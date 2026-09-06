package composition

// Install calls slogx.Setup, whose documented precondition is that it precede any slog
// call that matters: a line logged above it goes out through the stdlib default handler,
// so it is neither logfmt nor level-controlled and cannot answer the `| logfmt` query it
// exists to serve.
//
// SCOPE: Build's OWN statements plus the callees registered in syncLoggingCallees. An
// unregistered one is invisible, so this is not a claim that nothing logs pre-Install.

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
	// Register only a callee whose position above Install is not FORCED. validateConfig
	// logs synchronously too, but Install reads <configDir>/config.json, so registering it
	// would redden this gate forever with no correct fix.
	"startKiroCLI": {},
}

// slogCallsBeforeInstall reports every logging call in Build's OWN statements — a direct
// slog call or a syncLoggingCallees member — positioned before logctl.Install. FuncLit
// bodies are SKIPPED: a closure declared above Install runs whenever its owner invokes it,
// so a lexical verdict on one would be wrong. It takes SOURCE rather than a path so the red
// check is a fixture. A missing Install is an ERROR, not zero violations, or renaming it
// would make this pass vacuously.
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

	// Collect positions first, so the comparison is over the whole body rather than over
	// the statements ahead of a running cursor.
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
	// Without this the claim is vacuous: an empty Build body satisfies the ordering.
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
			// The real body's pre-Install statements include if blocks, which a walk over
			// top-level statements alone would miss.
			name: "a nested call before Install is caught too",
			src: "package composition\n\nfunc Build() {\n\tif true {\n" +
				"\t\tslog.Warn(\"x\")\n\t}\n\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 1,
		},
		{
			// A closure's declaration position says nothing about when it logs, and
			// "move the call below Install" is no remedy for one.
			name: "a slog call inside a pre-Install closure is not reported",
			src: "package composition\n\nfunc Build() {\n" +
				"\tstore.SetOnChange(func() {\n\t\tslog.Warn(\"x\")\n\t})\n" +
				"\tlogctl.Install(nil, \"\")\n}\n",
			wantCount: 0,
		},
		{
			// A registered callee logs on Build's own goroutine, so its position is
			// judged like a direct slog call.
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
