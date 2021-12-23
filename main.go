package main

import (
	"flag"
	"fmt"
	"go/build"
	"log"
	"os"
	"sort"
	"strings"
)

type PackageImports struct {
	Name           string
	Package        *build.Package
	Imports        []string
	PackageImports []*PackageImports
	Count          int
}

var (
	pkgTree *PackageImports = &PackageImports{}

	pkgs        map[string]*PackageImports
	erroredPkgs map[string]bool
	ids         map[string]string

	ignored = map[string]bool{
		"C": true,
	}
	ignoredPrefixes []string
	onlyPrefixes    []string

	ignoreStdlib   = flag.Bool("nostdlib", false, "ignore packages in the Go standard library")
	ignoreVendor   = flag.Bool("novendor", false, "ignore packages in the vendor directory")
	stopOnError    = flag.Bool("stoponerror", true, "stop on package import errors")
	withGoroot     = flag.Bool("withgoroot", false, "show dependencies of packages in the Go standard library")
	ignorePrefixes = flag.String("ignoreprefixes", "", "a comma-separated list of prefixes to ignore")
	ignorePackages = flag.String("ignorepackages", "", "a comma-separated list of packages to ignore")
	onlyPrefix     = flag.String("onlyprefixes", "", "a comma-separated list of prefixes to include")
	tagList        = flag.String("tags", "", "a comma-separated list of build tags to consider satisfied during the build")
	horizontal     = flag.Bool("horizontal", false, "lay out the dependency graph horizontally instead of vertically")
	withTests      = flag.Bool("withtests", false, "include test packages")
	maxLevel       = flag.Int("maxlevel", 256, "max level of go dependency graph")

	buildTags    []string
	buildContext = build.Default
)

func init() {
	flag.BoolVar(ignoreStdlib, "s", false, "(alias for -nostdlib) ignore packages in the Go standard library")
	flag.StringVar(ignorePrefixes, "p", "", "(alias for -ignoreprefixes) a comma-separated list of prefixes to ignore")
	flag.StringVar(ignorePackages, "i", "", "(alias for -ignorepackages) a comma-separated list of packages to ignore")
	flag.StringVar(onlyPrefix, "o", "", "(alias for -onlyprefixes) a comma-separated list of prefixes to include")
	flag.BoolVar(withTests, "t", false, "(alias for -withtests) include test packages")
	flag.IntVar(maxLevel, "l", 256, "(alias for -maxlevel) maximum level of the go dependency graph")
	flag.BoolVar(withGoroot, "d", false, "(alias for -withgoroot) show dependencies of packages in the Go standard library")
}

func main() {
	pkgs = make(map[string]*PackageImports)
	erroredPkgs = make(map[string]bool)
	ids = make(map[string]string)
	flag.Parse()

	args := flag.Args()

	if len(args) < 1 {
		log.Fatal("need one package name to process")
	}

	if *ignorePrefixes != "" {
		ignoredPrefixes = strings.Split(*ignorePrefixes, ",")
	}
	if *onlyPrefix != "" {
		onlyPrefixes = strings.Split(*onlyPrefix, ",")
	}
	if *ignorePackages != "" {
		for _, p := range strings.Split(*ignorePackages, ",") {
			ignored[p] = true
		}
	}
	if *tagList != "" {
		buildTags = strings.Split(*tagList, ",")
	}
	buildContext.BuildTags = buildTags

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to get cwd: %s", err)
	}
	// for _, a := range args {
	// 	if err := processPackage(cwd, a, 0, "", *stopOnError); err != nil {
	// 		log.Fatal(err)
	// 	}
	// }
	a := args[0]
	if err := processPackage(cwd, pkgTree, a, 0, "", *stopOnError); err != nil {
		log.Fatal(err)
	}

	listed := map[string]struct{}{}
	printTree(pkgTree, 0, listed)
}

func printTree(root *PackageImports, level int, listed map[string]struct{}) {
	if _, ok := listed[root.Name]; ok {
		return
	}
	for i := 0; i < level; i++ {
		fmt.Print(" |")
	}
	fmt.Printf("%v %d\n", root.Name, root.Count)
	listed[root.Name] = struct{}{}
	for _, pi := range root.PackageImports {
		printTree(pi, level+1, listed)
	}
}

func printGraphviz() {
	fmt.Println("digraph godep {")
	if *horizontal {
		fmt.Println(`rankdir="LR"`)
	}
	fmt.Print(`splines=ortho
nodesep=0.4
ranksep=0.8
node [shape="box",style="rounded,filled"]
edge [arrowsize="0.5"]
`)

	// sort packages
	pkgKeys := []string{}
	for k := range pkgs {
		pkgKeys = append(pkgKeys, k)
	}
	sort.Strings(pkgKeys)

	for _, pkgName := range pkgKeys {
		pi := pkgs[pkgName]
		pkg := pi.Package
		pkgId := getId(pkgName)

		if isIgnored(pkg) {
			continue
		}

		var color string
		switch {
		case pkg.Goroot:
			color = "palegreen"
		case len(pkg.CgoFiles) > 0:
			color = "darkgoldenrod1"
		case isVendored(pkg.ImportPath):
			color = "palegoldenrod"
		case hasBuildErrors(pkg):
			color = "red"
		default:
			color = "paleturquoise"
		}

		fmt.Printf("%s [label=\"%s\" color=\"%s\" URL=\"%s\" target=\"_blank\"];\n", pkgId, pkgName, color, pkgDocsURL(pkgName))

		// Don't render imports from packages in Goroot
		if pkg.Goroot && !*withGoroot {
			continue
		}

		for _, imp := range pi.PackageImports {
			impPkg := imp.Package
			if impPkg == nil || isIgnored(impPkg) {
				continue
			}

			impId := getId(imp.Name)
			fmt.Printf("%s -> %s;\n", pkgId, impId)
		}
	}
	fmt.Println("}")
}

func pkgDocsURL(pkgName string) string {
	return "https://godoc.org/" + pkgName
}

func processPackage(root string, curPackage *PackageImports, pkgName string, level int, importedBy string, stopOnError bool) error {
	if level++; level > *maxLevel {
		return nil
	}
	if ignored[pkgName] {
		return nil
	}

	pkg, buildErr := buildContext.Import(pkgName, root, 0)
	if buildErr != nil {
		if stopOnError {
			return fmt.Errorf("failed to import %s (imported at level %d by %s):\n%s", pkgName, level, importedBy, buildErr)
		}
	}

	if isIgnored(pkg) {
		return nil
	}

	importPath := normalizeVendor(pkgName)
	if buildErr != nil {
		erroredPkgs[importPath] = true
	}

	//pkgs[importPath] = pkg
	curPackage.Name = importPath
	curPackage.Package = pkg
	curPackage.Imports = getImports(pkg)
	curPackage.PackageImports = make([]*PackageImports, 0, len(curPackage.Imports))
	curPackage.Count = 1
	pkgs[importPath] = curPackage

	// Don't worry about dependencies for stdlib packages
	if pkg.Goroot && !*withGoroot {
		return nil
	}

	for _, imp := range curPackage.Imports {
		if pi, ok := pkgs[imp]; ok {
			pi.Count++
			curPackage.PackageImports = append(curPackage.PackageImports, pi)
		} else {
			// new package
			next := &PackageImports{}
			if err := processPackage(pkg.Dir, next, imp, level, pkgName, stopOnError); err != nil {
				return err
			}
			// this may not get filled in if the package is ignored
			if next.Name != "" {
				curPackage.PackageImports = append(curPackage.PackageImports, next)
			}
		}
	}
	return nil
}

func getImports(pkg *build.Package) []string {
	allImports := pkg.Imports
	if *withTests {
		allImports = append(allImports, pkg.TestImports...)
		allImports = append(allImports, pkg.XTestImports...)
	}
	var imports []string
	found := make(map[string]struct{})
	for _, imp := range allImports {
		if imp == normalizeVendor(pkg.ImportPath) {
			// Don't draw a self-reference when foo_test depends on foo.
			continue
		}
		if _, ok := found[imp]; ok {
			continue
		}
		found[imp] = struct{}{}
		imports = append(imports, imp)
	}
	return imports
}

func deriveNodeID(packageName string) string {
	//TODO: improve implementation?
	id := "\"" + packageName + "\""
	return id
}

func getId(name string) string {
	id, ok := ids[name]
	if !ok {
		id = deriveNodeID(name)
		ids[name] = id
	}
	return id
}

func hasPrefixes(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func isIgnored(pkg *build.Package) bool {
	if len(onlyPrefixes) > 0 && !hasPrefixes(normalizeVendor(pkg.ImportPath), onlyPrefixes) {
		return true
	}

	if *ignoreVendor && isVendored(pkg.ImportPath) {
		return true
	}
	return ignored[normalizeVendor(pkg.ImportPath)] || (pkg.Goroot && *ignoreStdlib) || hasPrefixes(normalizeVendor(pkg.ImportPath), ignoredPrefixes)
}

func hasBuildErrors(pkg *build.Package) bool {
	if len(erroredPkgs) == 0 {
		return false
	}

	v, ok := erroredPkgs[normalizeVendor(pkg.ImportPath)]
	if !ok {
		return false
	}
	return v
}

func debug(args ...interface{}) {
	fmt.Fprintln(os.Stderr, args...)
}

func debugf(s string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, s, args...)
}

func isVendored(path string) bool {
	return strings.Contains(path, "/vendor/")
}

func normalizeVendor(path string) string {
	pieces := strings.Split(path, "vendor/")
	return pieces[len(pieces)-1]
}
