// Package board renders the whole spec database as one self-contained HTML
// page. It feeds exclusively on the engine's reports — the same data the MCP
// tools serve — so there is no second data path to drift.
package board

import (
	"fmt"
	"html/template"
	"regexp"
	"sort"
	"strings"
	"time"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

type nodeView struct {
	ID       string
	Kind     string
	KindName string
	Title    string
	BodyHTML template.HTML
	Fresh    bool
	Problems []string
	Covers   []string
	Paths    []string
	Deps     []core.DepInfo
	Evidence *core.EvidenceInfo
	Children []nodeView
	Depth    int
	IsTest   bool
}

type storyView struct {
	ID       string
	Title    string
	BodyHTML template.HTML
	Status   string
	StatusCl string
	Fresh    bool
	ACs      []acView
	Children []nodeView
	Blocked  []string
	Paths    []string
}

type ccView struct {
	ID       string
	Title    string
	BodyHTML template.HTML
	Accepted bool
}

type acView struct {
	ID        string
	GivenHTML template.HTML
	WhenHTML  template.HTML
	ThenHTML  template.HTML
	CoveredBy []string
}

type termView struct {
	Term       string
	Anchor     string
	Definition string
}

type cardView struct {
	ID    string
	Title string
	Fresh bool
}

type columnView struct {
	Label string
	Cl    string
	Cards []cardView
}

type covView struct {
	TotalPct string
	Gaps     []covGap
}

type covGap struct {
	File string
	Pct  string
	Low  bool
}

type page struct {
	Project  string
	Desc     string
	Coverage *covView
	Stamp    string
	Columns  []columnView
	Stories  []storyView
	CCs      []ccView
	Glossary []termView
}

var kindNames = map[string]string{
	"story": "Story", "acceptance_test": "Acceptance test", "arch": "Architecture",
	"integration_test": "Integration test", "detail_design": "Detail design",
	"unit_test": "Unit test", "cross_cutting": "Cross-cutting",
}

// termifier marks glossary-term occurrences in already-escaped text with
// hover definitions linking to the glossary section.
type termifier struct {
	re      *regexp.Regexp
	anchors map[string]string // lowercased term -> anchor
	defs    map[string]string // lowercased term -> definition
}

func anchorFor(term string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(term) {
		if ('a' <= r && r <= 'z') || ('0' <= r && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return "gloss-" + b.String()
}

func newTermifier(terms []store.TermDef) *termifier {
	if len(terms) == 0 {
		return &termifier{}
	}
	sorted := append([]store.TermDef(nil), terms...)
	// Longest first so overlapping terms prefer the more specific one.
	sort.Slice(sorted, func(i, j int) bool { return len(sorted[i].Term) > len(sorted[j].Term) })
	tf := &termifier{anchors: map[string]string{}, defs: map[string]string{}}
	var alts []string
	for _, td := range sorted {
		// Match against escaped text: escape the term the same way.
		escaped := template.HTMLEscapeString(td.Term)
		alts = append(alts, regexp.QuoteMeta(escaped))
		key := strings.ToLower(td.Term)
		tf.anchors[key] = anchorFor(td.Term)
		tf.defs[key] = td.Definition
	}
	tf.re = regexp.MustCompile(`(?i)\b(` + strings.Join(alts, "|") + `)\b`)
	return tf
}

// markup escapes text and wraps term occurrences in glossary links.
func (tf *termifier) markup(text string) template.HTML {
	escaped := template.HTMLEscapeString(text)
	if tf.re == nil {
		return template.HTML(escaped)
	}
	out := tf.re.ReplaceAllStringFunc(escaped, func(m string) string {
		// m is escaped text; recover the lookup key from the unescaped form.
		key := strings.ToLower(htmlUnescape(m))
		anchor, ok := tf.anchors[key]
		if !ok {
			return m
		}
		return `<a class="term" href="#` + anchor + `" title="` +
			template.HTMLEscapeString(tf.defs[key]) + `">` + m + `</a>`
	})
	return template.HTML(out)
}

func htmlUnescape(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'")
	return r.Replace(s)
}

// Render produces the full board HTML for one project.
func Render(e *core.Engine) (string, error) {
	overview, err := e.Overview()
	if err != nil {
		return "", err
	}
	tf := newTermifier(overview.Glossary)
	p := page{Project: e.Project.ID, Desc: e.Project.Description, Stamp: time.Now().Format("2006-01-02 15:04")}
	if overview.Coverage != nil {
		cv := &covView{TotalPct: fmt.Sprintf("%.1f%%", overview.Coverage.TotalPct)}
		for _, g := range overview.Coverage.Gaps {
			cv.Gaps = append(cv.Gaps, covGap{File: g.File, Pct: fmt.Sprintf("%.0f%%", g.Pct), Low: g.Pct < 50})
		}
		p.Coverage = cv
	}

	for _, td := range overview.Glossary {
		p.Glossary = append(p.Glossary, termView{Term: td.Term, Anchor: anchorFor(td.Term), Definition: td.Definition})
	}
	for _, cc := range overview.CrossCutting {
		n, err := e.Node(cc.ID)
		if err != nil {
			return "", err
		}
		p.CCs = append(p.CCs, ccView{ID: cc.ID, Title: cc.Title, BodyHTML: tf.markup(n.Body), Accepted: cc.Accepted})
	}
	for _, s := range overview.Stories {
		tree, err := e.Tree(s.ID)
		if err != nil {
			return "", err
		}
		story, err := e.Node(s.ID)
		if err != nil {
			return "", err
		}
		sv := storyView{
			ID: s.ID, Title: s.Title, BodyHTML: tf.markup(story.Body), Status: s.Status,
			StatusCl: strings.ReplaceAll(s.Status, "_", ""), Fresh: tree.Story.Fresh,
			Blocked: tree.Integrity, Paths: story.Paths,
		}
		for _, ac := range tree.ACs {
			sv.ACs = append(sv.ACs, acView{ID: ac.ID, GivenHTML: tf.markup(ac.Given),
				WhenHTML: tf.markup(ac.When), ThenHTML: tf.markup(ac.Then), CoveredBy: ac.CoveredBy})
		}
		for _, c := range tree.Story.Children {
			cv, err := viewNode(e, tf, c, 1)
			if err != nil {
				return "", err
			}
			sv.Children = append(sv.Children, cv)
		}
		p.Stories = append(p.Stories, sv)
	}
	// Kanban columns in lifecycle order; every story lands in its column.
	order := []string{"todo", "refined", "in_progress", "done"}
	byStatus := map[string]*columnView{}
	p.Columns = make([]columnView, 0, len(order)) // fixed capacity: the pointers below must survive
	for _, st := range order {
		p.Columns = append(p.Columns, columnView{Label: strings.ReplaceAll(st, "_", " "), Cl: strings.ReplaceAll(st, "_", ""), Cards: []cardView{}})
		byStatus[st] = &p.Columns[len(p.Columns)-1]
	}
	for _, sv := range p.Stories {
		if col, ok := byStatus[sv.Status]; ok {
			col.Cards = append(col.Cards, cardView{ID: sv.ID, Title: sv.Title, Fresh: sv.Fresh})
		}
	}
	var b strings.Builder
	if err := tmpl.Execute(&b, p); err != nil {
		return "", err
	}
	return b.String(), nil
}

func viewNode(e *core.Engine, tf *termifier, tn core.TreeNode, depth int) (nodeView, error) {
	full, err := e.Node(tn.ID)
	if err != nil {
		return nodeView{}, err
	}
	nv := nodeView{
		ID: tn.ID, Kind: tn.Kind, KindName: kindNames[tn.Kind], Title: tn.Title,
		BodyHTML: tf.markup(full.Body), Fresh: tn.Fresh, Problems: tn.Problems, Covers: tn.Covers,
		Paths: tn.Paths, Deps: tn.Deps, Evidence: tn.Evidence, Depth: depth,
		IsTest: model.TestSpecKinds[model.Kind(tn.Kind)],
	}
	if full.Body == "" {
		nv.BodyHTML = ""
	}
	for _, c := range tn.Children {
		cv, err := viewNode(e, tf, c, depth+1)
		if err != nil {
			return nodeView{}, err
		}
		nv.Children = append(nv.Children, cv)
	}
	return nv, nil
}

var tmpl = template.Must(template.New("board").Funcs(template.FuncMap{
	"join": func(v []string) string { return strings.Join(v, ", ") },
	"depthClass": func(d int) string {
		if d > 3 {
			d = 3
		}
		return fmt.Sprintf("d%d", d)
	},
}).Parse(boardHTML))

const boardHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>trellis board — {{.Project}}</title>
<link rel="stylesheet" href="https://fonts.googleapis.com/css2?family=Bitter:wght@500;600;700&family=Source+Sans+3:wght@400;600&family=IBM+Plex+Mono:wght@400;500&display=swap">
<style>
:root {
  --ground: #F7F8F6; --surface: #FFFFFF; --ink: #1C221D; --muted: #5B6560;
  --line: #DDE2DC; --accent: #2F6B3F; --accent-soft: #E7EFE8;
  --todo: #5B6560; --refined: #3D5A99; --inprogress: #A66A1E; --done: #2F6B3F; --stale: #A8352E;
  --stale-bg: #F6E4E2;
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --ground: #141815; --surface: #1C221E; --ink: #E4EAE4; --muted: #97A29A;
    --line: #313A33; --accent: #7FB88A; --accent-soft: #243227;
    --todo: #97A29A; --refined: #93ACDD; --inprogress: #D9A867; --done: #7FB88A; --stale: #DD8B84;
    --stale-bg: #362321;
  }
}
:root[data-theme="dark"] {
  --ground: #141815; --surface: #1C221E; --ink: #E4EAE4; --muted: #97A29A;
  --line: #313A33; --accent: #7FB88A; --accent-soft: #243227;
  --todo: #97A29A; --refined: #93ACDD; --inprogress: #D9A867; --done: #7FB88A; --stale: #DD8B84;
  --stale-bg: #362321;
}
* { box-sizing: border-box; }
body { background: var(--ground); color: var(--ink); margin: 0; font: 16px/1.55 "Source Sans 3", system-ui, sans-serif; }
.wrap { max-width: 900px; margin: 0 auto; padding: 40px 24px 80px; }
.mono { font-family: "IBM Plex Mono", ui-monospace, monospace; font-size: 0.86em; }
h1 { font-family: Bitter, Georgia, serif; font-size: 1.9rem; margin: 0; }
h1 span { color: var(--accent); }
.desc { color: var(--ink); margin: 8px 0 0; max-width: 65ch; }
.sub { color: var(--muted); margin: 4px 0 28px; font-size: 0.9rem; }
h2 { font-family: Bitter, Georgia, serif; font-size: 1.25rem; margin: 0 0 10px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
section { background: var(--surface); border: 1px solid var(--line); border-radius: 6px; padding: 22px 24px; margin: 0 0 22px; }
.chips { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 26px; }
.chip { display: inline-flex; gap: 7px; padding: 5px 11px; border-radius: 999px; border: 1px solid var(--line); background: var(--surface); color: var(--ink); text-decoration: none; font-size: 0.85rem; }
.state { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; }
.st-todo { color: var(--todo); } .st-refined { color: var(--refined); }
.st-inprogress { color: var(--inprogress); } .st-done { color: var(--done); }
.storybody { color: var(--muted); max-width: 65ch; margin: 0 0 16px; }
table { border-collapse: collapse; width: 100%; margin: 0 0 18px; font-size: 0.92rem; }
th { text-align: left; font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; color: var(--muted); padding: 0 12px 6px 0; border-bottom: 1px solid var(--line); }
td { padding: 10px 12px 10px 0; border-bottom: 1px solid var(--line); vertical-align: top; }
tr:last-child td { border-bottom: none; }
.gwt { display: inline-block; min-width: 46px; font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; color: var(--accent); }
.cov { color: var(--accent); white-space: nowrap; }
.node { border-left: 2px solid var(--line); padding: 2px 0 2px 14px; margin: 6px 0; }
.node.d2 { margin-left: 26px; } .node.d3 { margin-left: 52px; }
.node summary { cursor: pointer; list-style: none; }
.node summary::-webkit-details-marker { display: none; }
.nid { color: var(--accent); font-weight: 500; margin-right: 8px; }
.kind { font-size: 0.7rem; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 600; color: var(--muted); background: var(--accent-soft); border-radius: 3px; padding: 2px 6px; margin-right: 8px; }
.mark { font-size: 0.72rem; font-weight: 600; margin-left: 6px; }
.ok { color: var(--done); } .stale { color: var(--stale); }
.meta { color: var(--muted); font-size: 0.85rem; }
.body { color: var(--muted); font-size: 0.92rem; max-width: 68ch; margin: 8px 0 4px; white-space: pre-line; }
.problem { color: var(--stale); background: var(--stale-bg); border-radius: 4px; padding: 6px 10px; margin: 6px 0; font-size: 0.88rem; }
.evidence { color: var(--muted); font-size: 0.85rem; margin: 4px 0; }
.evidence .mono { color: var(--done); }
.gates { font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; font-weight: 700; }
a { color: var(--accent); }
a.term { color: inherit; text-decoration: underline dotted var(--accent); text-underline-offset: 2px; }
.kanban { display: grid; grid-template-columns: repeat(4, 1fr); gap: 12px; margin: 0 0 28px; }
@media (max-width: 720px) { .kanban { grid-template-columns: repeat(2, 1fr); } }
.col { background: var(--surface); border: 1px solid var(--line); border-radius: 6px; padding: 10px; min-width: 0; }
.colh { margin: 0 0 8px; font-size: 0.72rem; text-transform: uppercase; letter-spacing: 0.06em; display: flex; justify-content: space-between; }
.count { color: var(--muted); font-weight: 400; }
.scard { display: block; background: var(--ground); border: 1px solid var(--line); border-radius: 5px; padding: 8px 10px; margin: 0 0 8px; color: var(--ink); text-decoration: none; font-size: 0.88rem; line-height: 1.35; }
.scard:hover { border-color: var(--accent); }
.scard .mono { color: var(--accent); display: block; margin-bottom: 2px; }
details.story { background: var(--surface); border: 1px solid var(--line); border-radius: 6px; padding: 18px 24px; margin: 0 0 14px; }
details.story > summary { cursor: pointer; list-style: none; font-family: Bitter, Georgia, serif; font-size: 1.15rem; font-weight: 600; }
details.story > summary::-webkit-details-marker { display: none; }
details.story[open] > summary { margin-bottom: 12px; }
a.term:hover { color: var(--accent); }
</style></head><body>
<div class="wrap">
<h1><span>trellis</span> board</h1>
{{if .Desc}}<p class="desc">{{.Desc}}</p>{{end}}
<p class="sub">Project <span class="mono">{{.Project}}</span> · generated {{.Stamp}}</p>
<div class="kanban">
{{range .Columns}}<div class="col"><h3 class="colh st-{{.Cl}}">{{.Label}}<span class="count">{{len .Cards}}</span></h3>
{{range .Cards}}<a class="scard" href="#{{.ID}}"><span class="mono">{{.ID}}</span> {{.Title}}{{if not .Fresh}} <span class="mark stale">stale</span>{{end}}</a>
{{end}}</div>{{end}}
</div>

{{if .Coverage}}<section id="coverage"><h2>Coverage <span class="state">{{.Coverage.TotalPct}}</span></h2>
{{if .Coverage.Gaps}}<table><thead><tr><th>Largest gaps</th><th></th></tr></thead><tbody>
{{range .Coverage.Gaps}}<tr><td class="mono">{{.File}}</td><td class="{{if .Low}}stale{{else}}covpct{{end}}">{{.Pct}}</td></tr>
{{end}}</tbody></table>{{else}}<p class="meta">no gaps — every measured file fully covered</p>{{end}}
<p class="meta">observability, not a gate: closing a gap stays a judgment call</p></section>{{end}}

{{if .Glossary}}<section id="glossary"><h2>Glossary</h2><table><tbody>
{{range .Glossary}}<tr id="{{.Anchor}}"><td class="mono">{{.Term}}</td><td>{{.Definition}}</td></tr>
{{end}}</tbody></table></section>{{end}}

<section><h2>Cross-cutting architecture</h2>
{{range .CCs}}<details class="node d1" id="{{.ID}}" open><summary><span class="mono nid">{{.ID}}</span><span class="kind">Cross-cutting</span> {{.Title}} {{if .Accepted}}<span class="mark ok">accepted</span>{{else}}<span class="mark stale">draft / stale</span>{{end}}</summary><div class="body">{{.BodyHTML}}</div></details>
{{end}}</section>

{{range .Stories}}
<details class="story" id="{{.ID}}">
<summary><span class="mono nid">{{.ID}}</span> {{.Title}} <span class="state st-{{.StatusCl}}">{{.Status}}</span>{{if .Fresh}}<span class="mark ok">approved</span>{{else}}<span class="mark stale">stale</span>{{end}}</summary>
<p class="storybody">{{.BodyHTML}}</p>
{{if .Paths}}<p class="meta">code: <span class="mono">{{join .Paths}}</span></p>{{end}}
{{if .ACs}}<table><thead><tr><th>AC</th><th>Criterion</th><th>Covered by</th></tr></thead><tbody>
{{range .ACs}}<tr><td class="mono">{{.ID}}</td><td><span class="gwt">Given</span> {{.GivenHTML}}<br><span class="gwt">When</span> {{.WhenHTML}}<br><span class="gwt">Then</span> {{.ThenHTML}}</td><td class="mono cov">{{join .CoveredBy}}</td></tr>
{{end}}</tbody></table>{{end}}
{{range .Children}}{{template "node" .}}{{end}}
{{if .Blocked}}<p class="gates stale">blocked</p>{{range .Blocked}}<div class="problem">{{.}}</div>{{end}}{{else}}<p class="gates ok">gates open</p>{{end}}
</details>
{{end}}
</div>
<script>
function openTarget() {
	var id = location.hash && location.hash.slice(1);
	if (!id) return;
	var el = document.getElementById(id);
	var walk = el;
	while (walk) { if (walk.tagName === 'DETAILS') walk.open = true; walk = walk.parentElement; }
	if (el) el.scrollIntoView();
}
window.addEventListener('hashchange', openTarget);
openTarget();
</script>
</body></html>

{{define "node"}}<details class="node {{depthClass .Depth}}" id="{{.ID}}"{{if lt .Depth 2}} open{{end}}>
<summary><span class="mono nid">{{.ID}}</span><span class="kind">{{.KindName}}</span> {{.Title}}
{{if .Fresh}}<span class="mark ok">approved</span>{{else}}<span class="mark stale">stale</span>{{end}}
<span class="meta">{{if .Covers}} · covers <span class="mono">{{join .Covers}}</span>{{end}}{{range .Deps}} · needs <a class="mono {{if .Fresh}}ok{{else}}stale{{end}}" href="#{{.Target}}">{{.Target}}</a>{{end}}</span></summary>
{{if .BodyHTML}}<div class="body">{{.BodyHTML}}</div>{{end}}
{{if .Evidence}}<div class="evidence">proved by <span class="mono">{{join .Evidence.Tests}}</span> · {{.Evidence.RecordedAt}}</div>{{else if .IsTest}}<div class="evidence stale">no test evidence recorded yet</div>{{end}}
{{range .Problems}}<div class="problem">{{.}}</div>{{end}}
</details>
{{range .Children}}{{template "node" .}}{{end}}{{end}}
`
