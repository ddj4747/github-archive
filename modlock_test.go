package githubarchive

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

const officialGo = `C:\Program Files\Go\bin\go.exe`

func TestModlock(t *testing.T) {
	if prev := sqlite.StrictPragmas(true); !prev {
		t.Fatal("sqlite.StrictPragmas(true) returned false; init did not enable it")
	}

	if _, err := os.Stat(officialGo); err != nil {
		t.Fatalf("go binary: %v", err)
	}
	root := moduleRoot(t)
	cache := strings.TrimSpace(string(runGo(t, root, officialGo, "env", "GOMODCACHE")))
	if cache == "" {
		t.Fatal("go env GOMODCACHE returned an empty path")
	}
	out := runGo(t, root, officialGo, "list", "-mod=readonly", "-m", "-json", "all")
	mods := decodeModules(t, out)
	mainDir := ""
	for _, mod := range mods {
		if !mod.Main {
			continue
		}
		if mainDir != "" {
			t.Fatal("module graph contains more than one main module")
		}
		mainDir = mod.Dir
	}
	if mainDir == "" {
		mainDir = root
	}

	direct := make([]moduleVersion, 0, 2)
	got := make([]moduleIdentity, 0, len(mods))
	for _, mod := range mods {
		if mod.Path == "" {
			t.Fatal("module graph entry has an empty path")
		}
		if forbiddenModule(mod.Path) {
			t.Fatalf("module graph contains %s", mod.Path)
		}
		checkModuleDir(t, mod.Path, mod.Dir, mainDir, cache)
		if mod.Replace != nil {
			if mod.Replace.Path != "" && forbiddenModule(mod.Replace.Path) {
				t.Fatalf("module %s replacement path %s is forbidden", mod.Path, mod.Replace.Path)
			}
			checkModuleDir(t, mod.Path+" replacement", mod.Replace.Dir, mainDir, cache)
			t.Fatalf("module %s@%s is replaced by %s@%s", mod.Path, mod.Version, mod.Replace.Path, mod.Replace.Version)
		}
		if !mod.Main && mod.Sum == "" {
			t.Fatalf("module %s@%s has no zip sum", mod.Path, mod.Version)
		}
		if !mod.Main && !mod.Indirect {
			direct = append(direct, moduleVersion{Path: mod.Path, Version: mod.Version})
		}
		got = append(got, moduleIdentity{Path: mod.Path, Version: mod.Version, Sum: mod.Sum})
	}

	wantDirect := []moduleVersion{
		{Path: "gopkg.in/yaml.v3", Version: "v3.0.1"},
		{Path: "modernc.org/sqlite", Version: "v1.63.0"},
	}
	slices.SortFunc(direct, func(a, b moduleVersion) int {
		return strings.Compare(a.Path, b.Path)
	})
	if !slices.Equal(direct, wantDirect) {
		t.Fatalf("direct requirements = %+v, want %+v", direct, wantDirect)
	}

	want := slices.Clone(allowedModules)
	slices.SortFunc(got, compareIdentity)
	slices.SortFunc(want, compareIdentity)
	if !slices.Equal(got, want) {
		t.Fatalf("module graph = %+v, allowlist = %+v", got, want)
	}
}

// allowedModules is the exact go list -mod=readonly -m -json all graph.
// Sum is the module zip h1. The main module has no sum. These literals are
// not loaded from go.sum.
var allowedModules = []moduleIdentity{
	{Path: "githubarchive"},
	{Path: "github.com/dustin/go-humanize", Version: "v1.1.0", Sum: "h1:dbKTrvD0klcbBV/h4AWJdMuZogJACoMlvWIWZ5b2xWg="},
	{Path: "github.com/google/pprof", Version: "v0.0.0-20261016150608-1facf8f91b50", Sum: "h1:PlNWJRxu5APSV0pWqfIsZ0qt9OOGHSWfZQ4zvx+WgWs="},
	{Path: "github.com/google/uuid", Version: "v1.6.0", Sum: "h1:NIvaJDMOsjHA8n1jAhLSgzrAzy1Hgr+hNrb57e+94F0="},
	{Path: "github.com/hashicorp/golang-lru/v2", Version: "v2.0.7", Sum: "h1:a+bsQ5rvGLjzHuww6tVxozPZFVghXaHOwFs4luLUK2k="},
	{Path: "github.com/mattn/go-isatty", Version: "v0.0.24", Sum: "h1:tGZZoVgT/KiqK1c8ocVLeDS8BSWMRd47J3Lbz7vsReI="},
	{Path: "github.com/ncruces/go-strftime", Version: "v1.1.0", Sum: "h1:vtPmPWMkb539RGV9WpWT9EwJGvrOCKvMNI4obWL47Hk="},
	{Path: "github.com/remyoudompheng/bigfft", Version: "v0.0.0-20230129092748-24d4a6f8daec", Sum: "h1:W09IVJc94icq4NjY3clb7Lk8O1qJ8BdBEF8z0ibU0rE="},
	{Path: "golang.org/x/mod", Version: "v0.42.0", Sum: "h1:ICeL3t2L30ljSPHHxDnR4fqVcBt/zR2zJAb9juavmQg="},
	{Path: "golang.org/x/sync", Version: "v0.24.0", Sum: "h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA="},
	{Path: "golang.org/x/sys", Version: "v0.49.0", Sum: "h1:XbzkgYJHdqh/8m2Uu0W/dQv8nktxx4BFHp1M0gROTXA="},
	{Path: "golang.org/x/tools", Version: "v0.52.0", Sum: "h1:ryxg4U63oaUYk7uOLTF37n43f4qgtGvqV4Ioit1BaEs="},
	{Path: "gopkg.in/check.v1", Version: "v0.0.0-20161208181325-20d25e280405", Sum: "h1:yhCVgyC4o1eVCa2tZl7eS0r+SDo693bJlVdllGtEeKM="},
	{Path: "gopkg.in/yaml.v3", Version: "v3.0.1", Sum: "h1:fxVm/GzAzEWqLHuvctI91KS9hhNmmWOoWu0XTYJS7CA="},
	{Path: "modernc.org/cc/v4", Version: "v4.30.6-0.20261101114202-1b9d1a076f6b", Sum: "h1:WE5SLOURcNd8s6uPR/n4WV4weQxxYKi5OpEZkfNVvdE="},
	{Path: "modernc.org/ccgo/v4", Version: "v4.38.1", Sum: "h1:wdxJVBuFT8JufZnnQyS6Bbz3NOWhViD0xJOGR96Bpd4="},
	{Path: "modernc.org/fileutil", Version: "v1.4.0", Sum: "h1:j6ZzNTftVS054gi281TyLjHPp6CPHr2KCxEXjEbD6SM="},
	{Path: "modernc.org/gc/v2", Version: "v2.6.5", Sum: "h1:nyqdV8q46KvTpZlsw66kWqwXRHdjIlJOhG6kxiV/9xI="},
	{Path: "modernc.org/gc/v3", Version: "v3.2.0", Sum: "h1:/Y3LGDMQaoYf55PebP1qsZ5FDGgAZyG1RYKEHeQzh1o="},
	{Path: "modernc.org/goabi0", Version: "v0.2.0", Sum: "h1:HvEowk7LxcPd0eq6mVOAEMai46V+i7Jrj13t4AzuNks="},
	{Path: "modernc.org/libc", Version: "v1.80.1", Sum: "h1:ejn7HtTV3GknNKbklqWQ1nvUrfX8mTxs1D+xp/JQwy4="},
	{Path: "modernc.org/mathutil", Version: "v1.7.1", Sum: "h1:GCZVGXdaN8gTqB1Mf/usp1Y/hSqgI2vAGGP4jZMCxOU="},
	{Path: "modernc.org/memory", Version: "v1.12.1", Sum: "h1:nFMiWrpStgZczNl6XI9GnIk/rWhYIyHGUaR04pGbp9g="},
	{Path: "modernc.org/opt", Version: "v0.2.0", Sum: "h1:tGyef5ApycA7FSEOMraay9SaTk5zmbx7Tu+cJs4QKZg="},
	{Path: "modernc.org/sortutil", Version: "v1.2.1", Sum: "h1:+xyoGf15mM3NMlPDnFqrteY07klSFxLElE2PVuWIJ7w="},
	{Path: "modernc.org/sqlite", Version: "v1.63.0", Sum: "h1:ClxazqRFNfr7XPHTVLbyzTVz2PjWPz+BfJNu2e8JDMs="},
	{Path: "modernc.org/strutil", Version: "v1.2.1", Sum: "h1:UneZBkQA+DX2Rp35KcM69cSsNES9ly8mQWD71HKlOA0="},
	{Path: "modernc.org/token", Version: "v1.1.0", Sum: "h1:Xl7Ap9dKaEs5kLoOQeQmPWevfnk/DM5qcLcYlA8ys6Y="},
}

type moduleVersion struct {
	Path    string
	Version string
}

type moduleIdentity struct {
	Path    string
	Version string
	Sum     string
}

func compareIdentity(a, b moduleIdentity) int {
	if c := strings.Compare(a.Path, b.Path); c != 0 {
		return c
	}
	if c := strings.Compare(a.Version, b.Version); c != 0 {
		return c
	}
	return strings.Compare(a.Sum, b.Sum)
}

type listedModule struct {
	Path     string
	Version  string
	Sum      string
	Indirect bool
	Dir      string
	Main     bool
	Replace  *listedReplace
}

type listedReplace struct {
	Path    string
	Version string
	Dir     string
}

func decodeModules(t *testing.T, out []byte) []listedModule {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	var mods []listedModule
	for {
		var mod listedModule
		err := dec.Decode(&mod)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode module graph: %v", err)
		}
		mods = append(mods, mod)
	}
	if len(mods) == 0 {
		t.Fatal("module graph is empty")
	}
	return mods
}

func checkModuleDir(t *testing.T, owner, dir, mainDir, cache string) {
	t.Helper()
	if dir == "" {
		return
	}
	cleaned := filepath.Clean(dir)
	if mainDir != "" && cleaned == filepath.Clean(mainDir) {
		return
	}
	if !dirInModuleCache(cleaned, cache) {
		t.Fatalf("%s dir %s is outside the module cache %s", owner, cleaned, filepath.Clean(cache))
	}
}

func dirInModuleCache(dir, cache string) bool {
	rel, err := filepath.Rel(filepath.Clean(cache), filepath.Clean(dir))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func runGo(t *testing.T, root, goBin string, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(goBin, args...)
	cmd.Dir = root
	cmd.Env = goEnv()
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, exit.Stderr)
		}
		t.Fatalf("go %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func goEnv() []string {
	env := make([]string, 0, 16)
	for _, key := range []string{
		"PATH",
		"SystemRoot",
		"USERPROFILE",
		"APPDATA",
		"LOCALAPPDATA",
		"HOMEDRIVE",
		"HOMEPATH",
	} {
		value, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		env = append(env, key+"="+value)
	}
	return append(env,
		"GOWORK=off",
		"GOSUMDB=sum.golang.org",
		"GOPROXY=https://proxy.golang.org,direct",
		"GOFLAGS=",
		"GONOSUMDB=",
		"GOPRIVATE=",
		"GOINSECURE=",
	)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func forbiddenModule(path string) bool {
	folded := strings.ToLower(path)
	const sqlite3 = "github.com/mattn/go-sqlite3"
	if folded == sqlite3 || strings.HasPrefix(folded, sqlite3+"/") {
		return true
	}
	if strings.HasPrefix(folded, "github.com/google/go-github") {
		return true
	}
	return strings.HasPrefix(folded, "github.com/go-git/go-git")
}
