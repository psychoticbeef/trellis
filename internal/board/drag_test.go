package board

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dop251/goja"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

type mapMoveFixture struct {
	engine           *core.Engine
	store            *store.Store
	storyID          string
	firstActivityID  string
	secondActivityID string
}

func newMapMoveFixture(t *testing.T) mapMoveFixture {
	t.Helper()
	e, st := liveSetup(t)
	story, err := e.CreateNode(model.KindStory, "", "move me", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	one, two := 1, 2
	first, err := e.CreateNodeWithPosition(model.KindActivity, "", "First", "", nil, &one)
	if err != nil {
		t.Fatal(err)
	}
	second, err := e.CreateNodeWithPosition(model.KindActivity, "", "Second", "", nil, &two)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.SetMapPosition(story.ID, first.ID, 1); err != nil {
		t.Fatal(err)
	}
	return mapMoveFixture{engine: e, store: st, storyID: story.ID, firstActivityID: first.ID, secondActivityID: second.ID}
}

func postMapPosition(t *testing.T, client *http.Client, url string, payload any) (int, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	res, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	response, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(response)
}

func placement(t *testing.T, st *store.Store, projectID, storyID string) model.Node {
	t.Helper()
	story, err := st.GetNode(projectID, storyID)
	if err != nil {
		t.Fatal(err)
	}
	return story
}

type dragRuntimeResult struct {
	Parent      string                 `json:"parent"`
	OriginOrder []string               `json:"origin_order"`
	Error       string                 `json:"error"`
	ErrorHidden bool                   `json:"error_hidden"`
	Draggable   bool                   `json:"draggable"`
	FetchCalls  []dragRuntimeFetchCall `json:"fetch_calls"`
}

type dragRuntimeFetchCall struct {
	URL     string         `json:"url"`
	Options map[string]any `json:"options"`
}

type dragRuntimeOptions struct {
	failure      string
	responseText string
	validTarget  bool
	endpoint     string
	beforeDrop   func()
	dragEnd      bool
}

func runDragClient(t *testing.T, failure, responseText string, validTarget bool) dragRuntimeResult {
	t.Helper()
	return runDragClientWithOptions(t, dragRuntimeOptions{failure: failure, responseText: responseText, validTarget: validTarget})
}

func runDragClientWithOptions(t *testing.T, options dragRuntimeOptions) dragRuntimeResult {
	t.Helper()
	vm := goja.New()
	prelude := `
class Element {
  constructor(name) { this.name=name; this.children=[]; this.parentNode=null; this.listeners={}; this.attrs={}; this.hidden=false; this.draggable=false; this.textContent=''; }
  get firstChild() { return this.children.length ? this.children[0] : null; }
  get nextSibling() { if (!this.parentNode) return null; var i=this.parentNode.children.indexOf(this); return this.parentNode.children[i+1] || null; }
  setAttribute(k,v) { this.attrs[k]=String(v); }
  getAttribute(k) { return this.attrs[k] || ''; }
  addEventListener(k,fn) { this.listeners[k]=fn; }
  removeChild(c) { var i=this.children.indexOf(c); if (i>=0) this.children.splice(i,1); c.parentNode=null; }
  appendChild(c) { if (c.parentNode) c.parentNode.removeChild(c); this.children.push(c); c.parentNode=this; return c; }
  insertBefore(c,n) { if (c.parentNode) c.parentNode.removeChild(c); var i=n ? this.children.indexOf(n) : -1; if (i<0) this.children.push(c); else this.children.splice(i,0,c); c.parentNode=this; return c; }
  closest(selector) { return selector==='[data-map-cell]' && this.attrs['data-map-cell'] ? this : null; }
  querySelectorAll(selector) { return selector==='.map-card' ? cards : []; }
}
var mapPanel=new Element('panel'), origin=new Element('origin'), destination=new Element('destination');
origin.setAttribute('data-map-cell','UA-1:1'); destination.setAttribute('data-map-cell','UA-2:2');
var card=new Element('card'), next=new Element('next'); card.setAttribute('data-story-open','US-1');
origin.appendChild(card); origin.appendChild(next); mapPanel.appendChild(origin); mapPanel.appendChild(destination);
var cards=[card];
var document={createElement:function(name){return new Element(name);},querySelector:function(selector){return selector==='[data-board-panel="map"]' ? mapPanel : null;}};
var fetchCalls=[];
`
	if options.endpoint == "" {
		prelude += `var fetchFailure=` + stringJSON(options.failure) + `, responseText=` + stringJSON(options.responseText) + `;
function fetch(url,options) { fetchCalls.push({url:url,options:options}); if(fetchFailure==='network') return Promise.reject(new Error(responseText)); return Promise.resolve({ok:fetchFailure==='',text:function(){return Promise.resolve(responseText);}}); }
`
	} else {
		client := http.DefaultClient
		endpoint := options.endpoint
		if err := vm.Set("goFetch", func(call goja.FunctionCall) goja.Value {
			relative := call.Argument(0).String()
			body := call.Argument(1).String()
			if relative != "map-position" {
				return vm.ToValue(map[string]any{"ok": false, "text": "unexpected URL " + relative})
			}
			request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
			if err != nil {
				return vm.ToValue(map[string]any{"ok": false, "text": err.Error()})
			}
			request.Header.Set("Content-Type", "application/json")
			response, err := client.Do(request)
			if err != nil {
				return vm.ToValue(map[string]any{"ok": false, "text": err.Error()})
			}
			defer response.Body.Close()
			responseBody, err := io.ReadAll(response.Body)
			if err != nil {
				return vm.ToValue(map[string]any{"ok": false, "text": err.Error()})
			}
			return vm.ToValue(map[string]any{"ok": response.StatusCode >= 200 && response.StatusCode < 300, "text": string(responseBody)})
		}); err != nil {
			t.Fatal(err)
		}
		prelude += `function fetch(url,options) { fetchCalls.push({url:url,options:options}); var response=goFetch(url,options.body); return Promise.resolve({ok:response.ok,text:function(){return Promise.resolve(response.text);}}); }
`
	}
	script := strings.TrimSuffix(strings.TrimPrefix(liveDragScript, "<script>"), "</script>")
	if _, err := vm.RunString(prelude + script + `card.listeners.dragstart({dataTransfer:{effectAllowed:'',setData:function(){}}});`); err != nil {
		t.Fatal(err)
	}
	if options.beforeDrop != nil {
		options.beforeDrop()
	}
	target := "destination"
	if !options.validTarget {
		target = "mapPanel"
	}
	if _, err := vm.RunString(`mapPanel.listeners.drop({target:` + target + `,preventDefault:function(){}});`); err != nil {
		t.Fatal(err)
	}
	if options.dragEnd {
		if _, err := vm.RunString(`card.listeners.dragend(); mapPanel.listeners.drop({target:destination,preventDefault:function(){}});`); err != nil {
			t.Fatal(err)
		}
	}
	value, err := vm.RunString(`JSON.stringify({parent:card.parentNode.name,origin_order:origin.children.map(function(c){return c.name;}),error:mapPanel.children[0].textContent,error_hidden:mapPanel.children[0].hidden,draggable:card.draggable,fetch_calls:fetchCalls});`)
	if err != nil {
		t.Fatal(err)
	}
	var result dragRuntimeResult
	if err := json.Unmarshal([]byte(value.String()), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func stringJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type staticRuntimeResult struct {
	MapVisible      bool `json:"map_visible"`
	OverviewHidden  bool `json:"overview_hidden"`
	StoryOpen       bool `json:"story_open"`
	OverviewInert   bool `json:"overview_inert"`
	CloseFocused    bool `json:"close_focused"`
	NetworkRequests int  `json:"network_requests"`
}

func exerciseStaticClient(t *testing.T, page string) staticRuntimeResult {
	t.Helper()
	start := strings.LastIndex(page, "<script>")
	if start < 0 {
		t.Fatal("static client script missing")
	}
	end := strings.Index(page[start:], "</script>")
	if end < 0 {
		t.Fatal("static client script end missing")
	}
	script := page[start+len("<script>") : start+end]
	prelude := `
class Element {
  constructor(name){this.name=name;this.attrs={};this.listeners={};this.hidden=false;this.inert=false;this.focused=false;this.parentElement=null;this.tagName='DIV';}
  setAttribute(k,v){this.attrs[k]=String(v);}
  getAttribute(k){return this.attrs[k]||'';}
  removeAttribute(k){delete this.attrs[k];}
  addEventListener(k,fn){this.listeners[k]=fn;}
  focus(){this.focused=true;document.activeElement=this;}
  scrollIntoView(){}
  closest(selector){
    if(selector==='[data-board-tab]' && this.attrs['data-board-tab']) return this;
    if(selector==='[data-story-open]' && this.attrs['data-story-open']) return this;
    if(selector==='[data-context-open]' && this.attrs['data-context-open']) return this;
    if(selector==='[data-modal-close]' && this.attrs['data-modal-close']!==undefined) return this;
    if(selector==='[data-modal]' && this.attrs['data-modal']!==undefined) return this;
    return null;
  }
  matches(selector){return selector==='[data-modal]' && this.attrs['data-modal']!==undefined;}
  contains(element){return element===this;}
  querySelector(selector){return selector==='[data-modal-close]' ? closeButton : null;}
  querySelectorAll(){return [];}
}
var overviewTab=new Element('overview-tab'), mapTab=new Element('map-tab');
overviewTab.setAttribute('data-board-tab','overview'); mapTab.setAttribute('data-board-tab','map');
var overviewPanel=new Element('overview-panel'), mapPanel=new Element('map-panel'); mapPanel.hidden=true;
var overview=new Element('overview'), storyModal=new Element('story-modal'), closeButton=new Element('close'), card=new Element('card');
storyModal.hidden=true; storyModal.setAttribute('data-modal',''); closeButton.setAttribute('data-modal-close',''); card.setAttribute('data-story-open','US-1');
var documentListeners={}, networkRequests=0;
var document={
  activeElement:null,
  body:{classList:{add:function(){},remove:function(){}}},
  getElementById:function(id){if(id==='board-overview')return overview;if(id==='story-US-1')return storyModal;return null;},
  querySelectorAll:function(selector){return selector==='[data-board-tab]'?[overviewTab,mapTab]:[];},
  querySelector:function(selector){if(selector==='[data-board-panel="overview"]')return overviewPanel;if(selector==='[data-board-panel="map"]')return mapPanel;return null;},
  addEventListener:function(type,fn){documentListeners[type]=fn;}
};
function fetch(){networkRequests++;return Promise.reject(new Error('static client attempted network'));}
`
	vm := goja.New()
	value, err := vm.RunString(prelude + script + `
mapTab.listeners.click();
documentListeners.click({target:card,preventDefault:function(){}});
JSON.stringify({map_visible:!mapPanel.hidden,overview_hidden:overviewPanel.hidden,story_open:!storyModal.hidden,overview_inert:overview.inert,close_focused:closeButton.focused,network_requests:networkRequests});`)
	if err != nil {
		t.Fatal(err)
	}
	var result staticRuntimeResult
	if err := json.Unmarshal([]byte(value.String()), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertDragScriptContract(t *testing.T, page string) {
	t.Helper()
	for _, want := range []string{
		`card.draggable = true`,
		`drag = {card: card, parent: card.parentNode, next: card.nextSibling}`,
		`event.target.closest('[data-map-cell]')`,
		`fetch('map-position'`,
		`method: 'POST'`,
		`story_id: current.card.getAttribute('data-story-open')`,
		`activity_id: destination.activity_id`,
		`slice: destination.slice`,
		`state.parent.insertBefore(state.card, state.next)`,
		`state.parent.appendChild(state.card)`,
		`errorBox.textContent = message`,
		`errorBox.setAttribute('role', 'alert')`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("live drag script missing %q", want)
		}
	}
}

// TestMapPositionHandler_UT_64 proves UT-64: live map-position dispatch is
// POST-only, strict, machine-readable, exhaustive, and delegates to Engine.
func TestMapPositionHandler_UT_64(t *testing.T) {
	fixture := newMapMoveFixture(t)
	srv := httptest.NewServer(Handler(fixture.engine, fixture.store))
	t.Cleanup(srv.Close)

	res, err := srv.Client().Get(srv.URL + "/map-position")
	if err != nil {
		t.Fatal(err)
	}
	methodBody, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusMethodNotAllowed || res.Header.Get("Allow") != http.MethodPost || !strings.Contains(string(methodBody), `"error":"method must be POST"`) {
		t.Fatalf("GET contract: status=%d allow=%q body=%s", res.StatusCode, res.Header.Get("Allow"), methodBody)
	}
	plain, _ := http.Post(srv.URL+"/map-position", "text/plain", strings.NewReader(`{"story_id":"`+fixture.storyID+`"}`))
	plain.Body.Close()
	if plain.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain status=%d", plain.StatusCode)
	}
	crossSite, _ := http.NewRequest(http.MethodPost, srv.URL+"/map-position", strings.NewReader(`{"story_id":"`+fixture.storyID+`"}`))
	crossSite.Header.Set("Content-Type", "application/json")
	crossSite.Header.Set("Origin", "https://hostile.invalid")
	crossResponse, err := srv.Client().Do(crossSite)
	if err != nil {
		t.Fatal(err)
	}
	crossResponse.Body.Close()
	if crossResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status=%d", crossResponse.StatusCode)
	}
	unknown := httptest.NewRecorder()
	MultiHandler(fixture.store).ServeHTTP(unknown, httptest.NewRequest(http.MethodPost, "/p/nope/map-position", strings.NewReader(`{}`)))
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("unknown project status=%d", unknown.Code)
	}

	status, body := postMapPosition(t, srv.Client(), srv.URL+"/map-position", map[string]any{
		"story_id": fixture.storyID, "activity_id": fixture.secondActivityID, "slice": 2, "extra": true,
	})
	if status != http.StatusBadRequest || !strings.Contains(body, "unknown field") {
		t.Fatalf("strict JSON contract: status=%d body=%s", status, body)
	}
	if got := placement(t, fixture.store, "p1", fixture.storyID); got.ActivityID != fixture.firstActivityID || got.Slice != 1 {
		t.Fatalf("malformed request mutated placement: %+v", got)
	}

	_, expectedErr := fixture.engine.SetMapPosition("missing-story", "missing-activity", 0)
	status, body = postMapPosition(t, srv.Client(), srv.URL+"/map-position", mapPositionRequest{
		StoryID: "missing-story", ActivityID: "missing-activity", Slice: 0,
	})
	if status != http.StatusBadRequest {
		t.Fatalf("engine rejection status=%d body=%s", status, body)
	}
	var failure map[string]string
	if err := json.Unmarshal([]byte(body), &failure); err != nil {
		t.Fatalf("failure is not JSON: %v: %s", err, body)
	}
	for _, want := range []string{"set_map_position rejected", "missing-story", "missing-activity", "slice must be at least 1"} {
		if !strings.Contains(failure["error"], want) {
			t.Errorf("exhaustive error missing %q: %q", want, failure["error"])
		}
	}
	if expectedErr == nil || failure["error"] != expectedErr.Error() {
		t.Fatalf("Engine error changed in transport:\nwant %q\n got %q", expectedErr, failure["error"])
	}
	for _, raw := range []string{`{"story_id":"` + fixture.storyID + `","activity_id":"` + fixture.secondActivityID + `","slice":1.5}`, `{} {}`} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/map-position", strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		invalid, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		invalid.Body.Close()
		if invalid.StatusCode != http.StatusBadRequest {
			t.Errorf("invalid JSON %q status=%d", raw, invalid.StatusCode)
		}
	}

	status, body = postMapPosition(t, srv.Client(), srv.URL+"/map-position", mapPositionRequest{
		StoryID: fixture.storyID, ActivityID: fixture.secondActivityID, Slice: 2,
	})
	if status != http.StatusNoContent || body != "" {
		t.Fatalf("success: status=%d body=%q", status, body)
	}
	if got := placement(t, fixture.store, "p1", fixture.storyID); got.ActivityID != fixture.secondActivityID || got.Slice != 2 {
		t.Fatalf("Engine.SetMapPosition result: %+v", got)
	}
}

// TestLiveDragClient_UT_65 proves UT-65: drag code exists only in live map
// pages and contains valid-target, one-POST, exact rollback, and error hooks.
func TestLiveDragClient_UT_65(t *testing.T) {
	fixture := newMapMoveFixture(t)
	static, err := Render(fixture.engine)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"card.draggable = true", "fetch('map-position'", "data-map-move-error"} {
		if strings.Contains(static, forbidden) {
			t.Fatalf("static export contains live capability %q", forbidden)
		}
	}
	live := liveHTML(static)
	assertDragScriptContract(t, live)
	if strings.Count(live, "fetch('map-position'") != 1 {
		t.Fatalf("live page has %d map-position fetches", strings.Count(live, "fetch('map-position'"))
	}
	if strings.Contains(live, "src=\"") || strings.Contains(live, "href=\"http") {
		t.Fatal("live drag capability references external asset")
	}

	empty, _ := liveSetup(t)
	withoutMap, err := Render(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(liveHTML(withoutMap), "card.draggable = true") {
		t.Fatal("project without story map gained drag output")
	}

	success := runDragClient(t, "", "", true)
	if success.Parent != "destination" || success.Draggable || len(success.FetchCalls) != 1 {
		t.Fatalf("executed success flow=%+v", success)
	}
	if success.FetchCalls[0].URL != "map-position" || success.FetchCalls[0].Options["method"] != "POST" {
		t.Fatalf("relative POST=%+v", success.FetchCalls[0])
	}
	failure := runDragClient(t, "http", `{"error":"first line\n- second line"}`, true)
	if failure.Parent != "origin" || strings.Join(failure.OriginOrder, ",") != "card,next" || failure.Error != "first line\n- second line" || !failure.Draggable {
		t.Fatalf("executed HTTP rollback=%+v", failure)
	}
	malformed := runDragClient(t, "http", "not JSON", true)
	if malformed.Parent != "origin" || malformed.Error != "not JSON" || strings.Join(malformed.OriginOrder, ",") != "card,next" {
		t.Fatalf("executed malformed-response rollback=%+v", malformed)
	}
	network := runDragClient(t, "network", "network down", true)
	if network.Parent != "origin" || network.Error != "network down" {
		t.Fatalf("executed network rollback=%+v", network)
	}
	invalid := runDragClientWithOptions(t, dragRuntimeOptions{validTarget: false, dragEnd: true})
	if invalid.Parent != "origin" || len(invalid.FetchCalls) != 0 {
		t.Fatalf("invalid/cancelled drag flow=%+v", invalid)
	}
}

// TestLiveMapPositionEndpoint_IT_54 proves IT-54: single and multi-project
// HTTP paths mutate through Engine, remain project-scoped, emit events, and
// serialize concurrent requests under existing project locking.
func TestLiveMapPositionEndpoint_IT_54(t *testing.T) {
	fixture := newMapMoveFixture(t)
	if err := fixture.store.CreateProject(store.Project{ID: "p2", Name: "second", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e2, err := core.NewEngine(fixture.store, "p2")
	if err != nil {
		t.Fatal(err)
	}
	p2Story, err := e2.CreateNode(model.KindStory, "", "other project", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	position := 1
	p2Activity, err := e2.CreateNodeWithPosition(model.KindActivity, "", "Other", "", nil, &position)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.SetMapPosition(p2Story.ID, p2Activity.ID, 1); err != nil {
		t.Fatal(err)
	}

	beforeSeq, err := fixture.store.MaxEventSeq("p1")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(MultiHandler(fixture.store))
	t.Cleanup(srv.Close)
	getResponse, err := srv.Client().Get(srv.URL + "/p/p1/map-position")
	if err != nil {
		t.Fatal(err)
	}
	getResponse.Body.Close()
	if getResponse.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET map-position status=%d", getResponse.StatusCode)
	}
	events, err := srv.Client().Get(srv.URL + "/p/p1/events")
	if err != nil {
		t.Fatal(err)
	}
	defer events.Body.Close()
	eventReader := bufio.NewReader(events.Body)
	eventReader.ReadString('\n')
	reload := make(chan string, 1)
	go func() {
		for {
			line, err := eventReader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data:") {
				reload <- strings.TrimSpace(line)
				return
			}
		}
	}()
	status, _ := postMapPosition(t, srv.Client(), srv.URL+"/p/p1/map-position", mapPositionRequest{
		StoryID: fixture.storyID, ActivityID: fixture.secondActivityID, Slice: 3,
	})
	if status != http.StatusNoContent {
		t.Fatalf("multi-project POST status=%d", status)
	}
	select {
	case event := <-reload:
		if event != "data: reload" {
			t.Fatalf("SSE event=%q", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("successful endpoint move produced no SSE reload")
	}
	afterSeq, _ := fixture.store.MaxEventSeq("p1")
	if afterSeq <= beforeSeq {
		t.Fatal("successful move did not append event for SSE")
	}
	if got := placement(t, fixture.store, "p2", p2Story.ID); got.ActivityID != p2Activity.ID || got.Slice != 1 {
		t.Fatalf("p1 POST changed p2: %+v", got)
	}
	beforeReject := placement(t, fixture.store, "p1", fixture.storyID)
	rejectStatus, rejectBody := postMapPosition(t, srv.Client(), srv.URL+"/p/p1/map-position", mapPositionRequest{
		StoryID: fixture.storyID, ActivityID: "missing-activity", Slice: 0,
	})
	var rejection map[string]string
	if rejectStatus != http.StatusBadRequest || json.Unmarshal([]byte(rejectBody), &rejection) != nil || !strings.Contains(rejection["error"], "missing-activity") || !strings.Contains(rejection["error"], "slice must be at least 1") {
		t.Fatalf("exhaustive integration rejection status=%d body=%s", rejectStatus, rejectBody)
	}
	afterReject := placement(t, fixture.store, "p1", fixture.storyID)
	if afterReject.ActivityID != beforeReject.ActivityID || afterReject.Rank != beforeReject.Rank || afterReject.Slice != beforeReject.Slice {
		t.Fatalf("invalid integration request mutated placement: before=%+v after=%+v", beforeReject, afterReject)
	}

	unlock, err := fixture.store.LockProject("p1")
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan int, 1)
	go func() {
		body, _ := json.Marshal(mapPositionRequest{StoryID: fixture.storyID, ActivityID: fixture.secondActivityID, Slice: 4})
		res, err := srv.Client().Post(srv.URL+"/p/p1/map-position", "application/json", bytes.NewReader(body))
		if err != nil {
			blocked <- 0
			return
		}
		res.Body.Close()
		blocked <- res.StatusCode
	}()
	select {
	case status := <-blocked:
		unlock()
		t.Fatalf("POST bypassed held project lock with status %d", status)
	case <-time.After(75 * time.Millisecond):
	}
	unlock()
	select {
	case status := <-blocked:
		if status != http.StatusNoContent {
			t.Fatalf("POST after lock release status=%d", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("POST stayed blocked after project lock release")
	}

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for _, slice := range []int{4, 5} {
		wg.Add(1)
		go func(slice int) {
			defer wg.Done()
			body, _ := json.Marshal(mapPositionRequest{StoryID: fixture.storyID, ActivityID: fixture.secondActivityID, Slice: slice})
			res, err := srv.Client().Post(srv.URL+"/p/p1/map-position", "application/json", bytes.NewReader(body))
			if err != nil {
				statuses <- 0
				return
			}
			res.Body.Close()
			statuses <- res.StatusCode
		}(slice)
	}
	wg.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusNoContent {
			t.Errorf("concurrent POST status=%d", status)
		}
	}
	if got := placement(t, fixture.store, "p1", fixture.storyID); got.Slice != 4 && got.Slice != 5 {
		t.Fatalf("concurrent moves left torn placement: %+v", got)
	}
}

// TestStaticAndLiveDragCapability_IT_55 proves IT-55 across shared static,
// single-project live, and multi-project live rendering paths.
func TestStaticAndLiveDragCapability_IT_55(t *testing.T) {
	fixture := newMapMoveFixture(t)
	static, err := Render(fixture.engine)
	if err != nil {
		t.Fatal(err)
	}
	if liveHTML(static) == static {
		t.Fatal("live path did not add scripts")
	}
	if strings.Contains(static, "map-position") || strings.Contains(static, "draggable") {
		t.Fatal("static path contains mutation capability")
	}

	for _, name := range []string{"single", "multi"} {
		t.Run(name, func(t *testing.T) {
			successFixture := newMapMoveFixture(t)
			handler := Handler(successFixture.engine, successFixture.store)
			path, endpoint := "/", "/map-position"
			if name == "multi" {
				handler = MultiHandler(successFixture.store)
				path, endpoint = "/p/p1/", "/p/p1/map-position"
			}
			srv := httptest.NewServer(handler)
			defer srv.Close()
			res, err := srv.Client().Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			page, _ := io.ReadAll(res.Body)
			res.Body.Close()
			assertDragScriptContract(t, string(page))
			success := runDragClientWithOptions(t, dragRuntimeOptions{validTarget: true, endpoint: srv.URL + endpoint})
			if success.Parent != "destination" || success.Error != "" || len(success.FetchCalls) != 1 {
				t.Fatalf("live success flow=%+v", success)
			}
			if got := placement(t, successFixture.store, "p1", successFixture.storyID); got.ActivityID != successFixture.secondActivityID || got.Slice != 2 {
				t.Fatalf("live success placement=%+v", got)
			}

			rejectedFixture := newMapMoveFixture(t)
			rejectedHandler := Handler(rejectedFixture.engine, rejectedFixture.store)
			rejectedEndpoint := "/map-position"
			if name == "multi" {
				rejectedHandler = MultiHandler(rejectedFixture.store)
				rejectedEndpoint = "/p/p1/map-position"
			}
			rejectedServer := httptest.NewServer(rejectedHandler)
			defer rejectedServer.Close()
			rejected := runDragClientWithOptions(t, dragRuntimeOptions{
				validTarget: true,
				endpoint:    rejectedServer.URL + rejectedEndpoint,
				beforeDrop: func() {
					if err := rejectedFixture.engine.DeleteNode(rejectedFixture.secondActivityID); err != nil {
						t.Fatal(err)
					}
				},
			})
			if rejected.Parent != "origin" || strings.Join(rejected.OriginOrder, ",") != "card,next" || !strings.Contains(rejected.Error, "unknown activity") {
				t.Fatalf("live rejection flow=%+v", rejected)
			}
		})
	}
}

// TestLiveStoryMapMoveAcceptance_AT_62 proves AC-1 through real live HTTP,
// project-scoped POST, per-project SSE, and fresh rendering.
func TestLiveStoryMapMoveAcceptance_AT_62(t *testing.T) {
	old := pollInterval
	pollInterval = 10 * time.Millisecond
	t.Cleanup(func() { pollInterval = old })
	fixture := newMapMoveFixture(t)
	if err := fixture.store.CreateProject(store.Project{ID: "p2", Name: "second", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e2, err := core.NewEngine(fixture.store, "p2")
	if err != nil {
		t.Fatal(err)
	}
	p2Story, err := e2.CreateNode(model.KindStory, "", "unchanged", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	position := 1
	p2Activity, err := e2.CreateNodeWithPosition(model.KindActivity, "", "Other", "", nil, &position)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e2.SetMapPosition(p2Story.ID, p2Activity.ID, 1); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(MultiHandler(fixture.store))
	t.Cleanup(srv.Close)

	events, err := srv.Client().Get(srv.URL + "/p/p1/events")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { events.Body.Close() })
	reader := bufio.NewReader(events.Body)
	if line, _ := reader.ReadString('\n'); !strings.HasPrefix(line, ":") {
		t.Fatalf("SSE prelude=%q", line)
	}
	tick := make(chan string, 1)
	go func() {
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.HasPrefix(line, "data:") {
				tick <- strings.TrimSpace(line)
				return
			}
		}
	}()

	client := runDragClientWithOptions(t, dragRuntimeOptions{
		validTarget: true,
		endpoint:    srv.URL + "/p/p1/map-position",
	})
	if len(client.FetchCalls) != 1 || client.FetchCalls[0].URL != "map-position" || client.Parent != "destination" || client.Error != "" {
		t.Fatalf("executed drag did not complete one relative live POST: %+v", client)
	}
	if got := placement(t, fixture.store, "p2", p2Story.ID); got.ActivityID != p2Activity.ID || got.Slice != 1 {
		t.Fatalf("project-scoped drop changed p2: %+v", got)
	}
	select {
	case got := <-tick:
		if got != "data: reload" {
			t.Fatalf("SSE=%q", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("move produced no per-project reload")
	}
	res, _ := srv.Client().Get(srv.URL + "/p/p1/")
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	cell := `data-map-cell="` + fixture.secondActivityID + `:2"`
	cellAt := strings.Index(string(page), cell)
	cardAt := strings.Index(string(page)[cellAt:], `data-story-open="`+fixture.storyID+`"`)
	endAt := strings.Index(string(page)[cellAt:], "</td>")
	if cellAt < 0 || cardAt < 0 || cardAt > endAt {
		t.Fatalf("reloaded board lacks moved card in %s", cell)
	}
}

// TestStaticStoryMapReadOnlyAcceptance_AT_63 proves AC-2: static board keeps
// map navigation but gains no draggable state, POST, route, or external asset.
func TestStaticStoryMapReadOnlyAcceptance_AT_63(t *testing.T) {
	fixture := newMapMoveFixture(t)
	page, err := Render(fixture.engine)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`data-board-tab="map"`, `data-story-open="` + fixture.storyID + `"`, `openStoryDetail(`} {
		if !strings.Contains(page, want) {
			t.Errorf("static Map tab lost %q", want)
		}
	}
	runtime := exerciseStaticClient(t, page)
	if !runtime.MapVisible || !runtime.OverviewHidden || !runtime.StoryOpen || !runtime.OverviewInert || !runtime.CloseFocused || runtime.NetworkRequests != 0 {
		t.Fatalf("executed static Map/detail client=%+v", runtime)
	}
	for _, forbidden := range []string{"draggable", "map-position", "method: 'POST'", "fetch("} {
		if strings.Contains(page, forbidden) {
			t.Errorf("static board contains %q", forbidden)
		}
	}
	if strings.Contains(page, `<script src=`) || strings.Contains(page, `<link rel=`) {
		t.Fatal("static board references external assets")
	}
}

// TestRejectedLiveMoveRollbackAcceptance_AT_64 proves AC-3: deleted-target
// rejection stays exhaustive and live client restores exact parent/order while
// displaying full returned text safely.
func TestRejectedLiveMoveRollbackAcceptance_AT_64(t *testing.T) {
	fixture := newMapMoveFixture(t)
	srv := httptest.NewServer(Handler(fixture.engine, fixture.store))
	t.Cleanup(srv.Close)

	page, err := Render(fixture.engine) // browser holds now-invalid target cell
	if err != nil {
		t.Fatal(err)
	}
	before := placement(t, fixture.store, "p1", fixture.storyID)
	runtime := runDragClientWithOptions(t, dragRuntimeOptions{
		validTarget: true,
		endpoint:    srv.URL + "/map-position",
		beforeDrop: func() {
			if err := fixture.engine.DeleteNode(fixture.secondActivityID); err != nil {
				t.Fatal(err)
			}
		},
	})
	for _, want := range []string{fixture.secondActivityID, fixture.firstActivityID, "unknown activity"} {
		if !strings.Contains(runtime.Error, want) {
			t.Errorf("full rejection missing %q: %s", want, runtime.Error)
		}
	}
	after := placement(t, fixture.store, "p1", fixture.storyID)
	if after.ActivityID != before.ActivityID || after.Rank != before.Rank || after.Slice != before.Slice {
		t.Fatalf("rejected move changed placement: before=%+v after=%+v", before, after)
	}

	live := liveHTML(page)
	assertDragScriptContract(t, live)
	if runtime.Parent != "origin" || strings.Join(runtime.OriginOrder, ",") != "card,next" || runtime.Error == "" {
		t.Fatalf("executed rejected-drop rollback=%+v", runtime)
	}
	restoreAt := strings.Index(live, "restore(current);")
	showAt := strings.Index(live, "showError(error.message);")
	if restoreAt < 0 || showAt < restoreAt {
		t.Fatal("failure path does not restore card before showing full error")
	}
}
