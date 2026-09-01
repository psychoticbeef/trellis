package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"trellis/internal/core"
	"trellis/internal/model"
	"trellis/internal/store"
)

func readMCPLine_US_42(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	result := make(chan string, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := reader.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		result <- line
	}()
	select {
	case line := <-result:
		return line
	case err := <-errCh:
		t.Fatalf("read MCP response: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for MCP response")
	}
	return ""
}

func assertOccupiedServeMCP_US_42(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	addr := listener.Addr().String()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdin, oldStdout, oldStderr := os.Stdin, os.Stdout, os.Stderr
	os.Stdin, os.Stdout, os.Stderr = stdinR, stdoutW, stderrW
	defer func() {
		os.Stdin, os.Stdout, os.Stderr = oldStdin, oldStdout, oldStderr
		stdinR.Close()
		stdoutR.Close()
		stderrR.Close()
	}()

	stderrDone := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(stderrR)
		stderrDone <- data
	}()
	serveDone := make(chan error, 1)
	go func() { serveDone <- cmdServe([]string{"--project", "p1", "--board-addr", addr}) }()

	initialize := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "acceptance", "version": "1"},
		},
	}
	encoded, _ := json.Marshal(initialize)
	if _, err := stdinW.Write(append(encoded, '\n')); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(stdoutR)
	initResponse := readMCPLine_US_42(t, reader)
	var initJSON map[string]any
	if json.Unmarshal([]byte(initResponse), &initJSON) != nil || initJSON["id"] != float64(1) || initJSON["result"] == nil {
		t.Fatalf("invalid MCP initialize response: %s", initResponse)
	}
	if _, err := io.WriteString(stdinW, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`+"\n"); err != nil {
		t.Fatal(err)
	}
	toolsResponse := readMCPLine_US_42(t, reader)
	var toolsJSON map[string]any
	if json.Unmarshal([]byte(toolsResponse), &toolsJSON) != nil || toolsJSON["id"] != float64(2) || toolsJSON["result"] == nil {
		t.Fatalf("MCP tools/list failed after board bind degradation: %s", toolsResponse)
	}

	stdinW.Close()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("serve after stdin EOF: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not stop after stdin EOF")
	}
	stdoutW.Close()
	stderrW.Close()
	stderr := string(<-stderrDone)
	wantWarning := "board address " + addr + " in use, serving MCP only"
	if !strings.Contains(stderr, wantWarning) {
		t.Fatalf("stderr missing %q: %s", wantWarning, stderr)
	}
}

// TestBoardBindWarning_UT_45 proves UT-45: address-in-use diagnostics use
// deterministic stderr text while other listen failures retain diagnostics.
func TestBoardBindWarning_UT_45(t *testing.T) {
	var output strings.Builder
	reportBoardListenError(&output, "127.0.0.1:7420", &net.OpError{Op: "listen", Net: "tcp", Err: syscall.EADDRINUSE})
	if got, want := output.String(), "trellis: board address 127.0.0.1:7420 in use, serving MCP only\n"; got != want {
		t.Fatalf("warning = %q, want %q", got, want)
	}
}

// TestBoardAndServePolishAcceptance_AT_46 proves AT-46 and US-42.AC-1
// through US-42.AC-3 through real board and serve CLI entrypoints.
func TestBoardAndServePolishAcceptance_AT_46(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "polish", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	e, err := core.NewEngine(st, "p1")
	if err != nil {
		t.Fatal(err)
	}
	story, err := e.CreateNode(model.KindStory, "", "polish story", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.AddCategorizedUsage(story.ID,
		core.TokenCategories{Input: 1113014, Output: 111000, CacheRead: 999, CacheWrite: 1000},
		core.TokenCategories{Input: 16400000, Output: 1500, CacheRead: 1000000, CacheWrite: 1499}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCoverage("p1", []store.CoverageRow{{File: "internal/board/board.go", Covered: 1, Total: 2}}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "board.html")
	if err := run([]string{"board", "p1", "-o", out}); err != nil {
		t.Fatal(err)
	}
	htmlBytes, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	html := string(htmlBytes)
	for _, want := range []string{
		`title="1113014">1.1M</td>`, `title="111000">111k</td>`,
		`title="999">999</td>`, `title="1000">1k</td>`,
		`title="16400000">16.4M</td>`, `title="1500">2k</td>`,
		`title="1000000">1.0M</td>`, `title="1499">1k</td>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("static board missing %q", want)
		}
	}
	if strings.Contains(html, "observability, not a gate: closing a gap stays a judgment call") {
		t.Error("static board exposes design-philosophy coverage commentary")
	}

	liveListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	liveAddr := liveListener.Addr().String()
	liveListener.Close()
	go run([]string{"board", "p1", "--serve", "--addr", liveAddr})
	var liveResponse *http.Response
	for i := 0; i < 50; i++ {
		liveResponse, err = http.Get("http://" + liveAddr + "/")
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("live board unavailable: %v", err)
	}
	liveBytes, err := io.ReadAll(liveResponse.Body)
	liveResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	live := string(liveBytes)
	if !strings.Contains(live, `title="1113014">1.1M</td>`) || strings.Contains(live, "observability, not a gate: closing a gap stays a judgment call") {
		t.Fatalf("live board polish missing")
	}

	assertOccupiedServeMCP_US_42(t)
}

// TestServeBindDegradation_IT_42 proves IT-42: occupied board binding emits
// stderr diagnostics while real MCP initialize and tools/list remain usable.
func TestServeBindDegradation_IT_42(t *testing.T) {
	t.Setenv("TRELLIS_DATA_DIR", t.TempDir())
	st, err := openStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProject(store.Project{ID: "p1", Name: "polish integration", BaseBranch: "develop"}); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	assertOccupiedServeMCP_US_42(t)
}
