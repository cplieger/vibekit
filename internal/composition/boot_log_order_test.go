package composition

// The ordering of Build's own log calls against logctl.Install, pinned because a
// comment was otherwise the only thing holding it and the line's whole purpose
// depends on it.
//
// Install calls slogx.Setup, which is what makes a vibekit line logfmt and wires the
// LevelVar the Debug-logs toggle drives, and its doc states the precondition: call it
// before any other slog call that matters. A call above it goes out through the stdlib
// default handler instead, so `boot paths resolved` — the one line naming config_dir,
// work_dir and kiro_home together, and the only thing that makes a misdirected boot
// diagnosable from its own output — would not answer the `| logfmt | config_dir=`
// query it exists to serve. That is a documented-contract violation a compile cannot
// catch and a reader moving the line back would not notice.
//
// SCOPE, because this proves less than it may read as: Build's OWN statements plus the
// callees registered in syncLoggingCallees. An UNregistered one is invisible, so this is
// still NOT a claim that nothing logs pre-Install: validateConfig warns from one call
// down, and its position is forced (Install reads <configDir>/config.json, so it cannot
// precede the check that the dir is usable).

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// syncLoggingCallees are the package-local functions Build calls that log on the CALLING
// goroutine, so their position against Install is judged like a direct slog call. A
// MANUAL allowlist, not transitive analysis: the walk follows no call graph, so a helper
// that logs and is registered nowhere here stays invisible and its ordering unpinned.
var syncLoggingCallees = map[string]struct{}{
	// Register on TWO conditions: a slog call ahead of any `go` statement, AND a position
	// above Install that is not forced. startKiroCLI meets both; validateConfig meets only
	// the first (Install reads <configDir>/config.json), so registering it would redden
	// this gate forever with no correct fix.
	"startKiroCLI": {},
}

// slogCallsBeforeInstall reports every logging call in Build's OWN statements that
// precedes the logctl.Install call, by position: a direct slog call, or a call to a
// syncLoggingCallees member. FuncLit bodies are SKIPPED: a closure declared above
// Install can run long after it (Build's own SetOnChange and OnStatus callbacks are
// that shape), so a lexical verdict on one would be wrong and the remedy the failure
// names — move the call below Install — is not available for a closure.
//
// It takes SOURCE rather than a path so the red check is a fixture rather than an edit
// to the file under test: a mutant string exercises the failing direction with nothing
// to revert. A missing Install is an ERROR, not zero violations, or renaming it would
// make this pass vacuously forever.
func slogCallsBeforeInstall(src string) (before []string, err error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "composition.go", src, 0)
	if err != nil {
		return nil, err
	}
	var body *ast.BlockStmt
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == "Build" && fn.Recv == nil {
			body = fn.Body
		}
	}
	if body == nil {
		return nil, errNoBuild
	}

	// One pass: the install position, then every logging position, so the comparison is
	// over the whole body rather than over the statements ahead of a running cursor.
	installPos := token.NoPos
	var logAt []token.Pos
	var logName []string
	ast.Inspect(body, func(n ast.Node) bool {
		if _, isClosure := n.(*ast.FuncLit); isClosure {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			if _, logs := syncLoggingCallees[fun.Name]; logs {
				logAt = append(logAt, call.Pos())
				logName = append(logName, fun.Name)
			}
		case *ast.SelectorExpr:
			pkg, ok := fun.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch {
			case pkg.Name == "logctl" && fun.Sel.Name == "Install":
				installPos = call.Pos()
			case pkg.Name == "slog":
				logAt = append(logAt, call.Pos())
				logName = append(logName, "slog."+fun.Sel.Name)
			}
		}
		return true
	})
	if !installPos.IsValid() {
		return nil, errNoInstall
	}
	for i, p := range logAt {
		if p < installPos {
			before = append(before, logName[i]+" at "+fset.Position(p).String())
		}
	}
	return before, nil
}

// errNoBuild and errNoInstall are the two ways this test stops meaning anything, so
// each is a sentinel the red check asserts on by identity rather than by message.
var (
	errNoBuild   = errors.New("no top-level func Build in the parsed source")
	errNoInstall = errors.New("no logctl.Install call inside Build")
)

func TestBuild_LogsOnlyAfterTheHandlerIsInstalled(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("composition.go")
	if err != nil {
		t.Fatalf("read composition.go: %v", err)
	}
	// The subject has to actually contain the line, or an empty Build body would
	// satisfy the ordering claim.
	if !strings.Contains(string(raw), `slog.Info("boot paths resolved"`) {
		t.Fatal(`composition.go carries no slog.Info("boot paths resolved") call; ` +
			"the boot-path diagnostic is what this ordering exists to protect")
	}

	before, err := slogCallsBeforeInstall(string(raw))
	if err != nil {
		t.Fatalf("inspect Build: %v", err)
	}
	if len(before) > 0 {
		t.Errorf("Build logs before logctl.Install: %v\n"+
			"Those lines go out through slog's stdlib default handler rather than "+
			"slogx's logfmt one, and ignore the configured level. A bare name is a "+
			"syncLoggingCallees member that logs one frame down. Move the call below "+
			"Install.", before)
	}
}

// TestSlogCallsBeforeInstall_ReadsTheOrdering is the red check for the predicate above,
// as fixtures rather than as an edit to composition.go.
func TestSlogCallsBeforeInstall_ReadsTheOrdering(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		src       string
		wantCount int
		wantErr   error
	}{
		{
			name: "after Install is clean",
			src: "package composition\n\nfunc Build() {\n" +
				"\tlogctl.Install(nil, \"\")\n\tslog.Info(\"boot paths resolved\")\n}\n",
			wantCount: 0,
		},
		{
			name: "before Install is caught",
			src: "package composition\n\nfunc Build() {\n" +
				"\tslog.Info(\"boot paths resolved\")\n\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 1,
		},
		{
			// Nested, because the real body's pre-Install statements include if blocks
			// and a walk over top-level statements alone would miss one planted there.
			name: "a nested call before Install is caught too",
			src: "package composition\n\nfunc Build() {\n\tif true {\n" +
				"\t\tslog.Warn(\"x\")\n\t}\n\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 1,
		},
		{
			// A closure DECLARED before Install runs whenever its owner invokes it,
			// so its position says nothing about when it logs — and "move the call
			// below Install" is not a remedy that exists for one.
			name: "a slog call inside a pre-Install closure is not reported",
			src: "package composition\n\nfunc Build() {\n" +
				"\tstore.SetOnChange(func() {\n\t\tslog.Warn(\"x\")\n\t})\n" +
				"\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 0,
		},
		{
			// A registered callee logs on Build's own goroutine one frame down, so
			// its position is judged exactly like a direct slog call.
			name: "a registered callee before Install is caught",
			src: "package composition\n\nfunc Build() {\n" +
				"\tkiro := startKiroCLI(nil, nil)\n\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 1,
		},
		{
			name: "the same callee after Install is clean",
			src: "package composition\n\nfunc Build() {\n" +
				"\tlogctl.Install(nil, \"\")\n\tkiro := startKiroCLI(nil, nil)\n}\n",
			wantCount: 0,
		},
		{
			// The limitation, pinned rather than left implied: no call graph is
			// followed, so a helper that logs and is not registered is invisible.
			name: "an unregistered callee before Install is not reported",
			src: "package composition\n\nfunc Build() {\n" +
				"\tvalidateConfig(nil, nil)\n\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 0,
		},
		{
			name:    "a renamed Install is an error, never a pass",
			src:     "package composition\n\nfunc Build() {\n\tslog.Info(\"x\")\n}\n",
			wantErr: errNoInstall,
		},
		{
			name:    "a renamed Build is an error too",
			src:     "package composition\n\nfunc build() {\n\tlogctl.Install(nil, \"\")\n}\n",
			wantErr: errNoBuild,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := slogCallsBeforeInstall(tc.src)
			if tc.wantErr != nil {
				if err != tc.wantErr {
					t.Fatalf("slogCallsBeforeInstall err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("slogCallsBeforeInstall(%q) err = %v, want nil", tc.name, err)
			}
			if len(got) != tc.wantCount {
				t.Errorf("slogCallsBeforeInstall reported %d pre-Install calls %v, want %d",
					len(got), got, tc.wantCount)
			}
		})
	}
}
