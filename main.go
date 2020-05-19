package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/juju/ansiterm"
	"github.com/urfave/cli/v2"
	"golang.org/x/tools/cover"
)

func main() {
	app := cli.App{
		Description: "Nice coverage output",
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "reverse",
				Aliases: []string{"r"},
			},
		},
		Action: func(c *cli.Context) error {
			reverse := c.Bool("reverse")
			file := c.Args().First()
			if file == "" {
				return errors.New("please provide a coverage file")
			}
			return run(file, reverse)
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Printf("Error: %s\n", err)
	}
}

func run(file string, reverse bool) error {
	tabber := ansiterm.NewTabWriter(os.Stdout, 1, 8, 1, '\t', 0)
	defer tabber.Flush()

	profiles, err := cover.ParseProfiles(file)
	if err != nil {
		return err
	}

	var total, covered int64
	dirs, err := findPkgs(profiles)
	if err != nil {
		return err
	}

	var coverfiles []coveredFile

	for _, profile := range profiles {
		var fileTotal, fileCovered int64
		f, err := findFile(dirs, profile.FileName)
		if err != nil {
			return err
		}
		fe, err := test(f)
		if err != nil {
			return err
		}

		for _, f := range fe {
			n, d := f.coverage(profile)
			fileTotal += d
			fileCovered += n
			total = total + d
			covered = covered + n
		}
		coverfiles = append(coverfiles, coveredFile{
			filename: f,
			percent:  100.0 * float64(fileCovered) / float64(fileTotal),
		})
	}
	sort.Slice(coverfiles, func(i, j int) bool {
		if reverse {
			return coverfiles[i].percent >= coverfiles[j].percent
		}
		return coverfiles[i].percent < coverfiles[j].percent
	})

	for _, coverfile := range coverfiles {
		tabber.SetForeground(getColor(coverfile.percent))
		fmt.Fprintf(tabber, "%s\t%.1f%%\n", coverfile.filename, coverfile.percent)
	}

	fmt.Fprintf(tabber, "\t\t\n")
	totalPercent := 100.0 * float64(covered) / float64(total)
	tabber.SetForeground(getColor(totalPercent))
	fmt.Fprintf(tabber, "Total:\t%.1f%%\n", totalPercent)

	return nil
}

func getColor(percent float64) ansiterm.Color {
	if percent <= 30 {
		return ansiterm.Red
	}
	if percent > 30 && percent <= 70 {
		return ansiterm.Yellow
	}
	if percent > 70 {
		return ansiterm.Green
	}

	return ansiterm.Default
}

type coveredFile struct {
	filename string
	percent  float64
}

func test(file string) ([]*FuncExtent, error) {
	fset := token.NewFileSet()
	parsedFile, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		return nil, err
	}

	visitor := &FuncVisitor{
		fset:    fset,
		name:    file,
		astFile: parsedFile,
	}

	ast.Walk(visitor, visitor.astFile)

	return visitor.funcs, nil
}

type FuncVisitor struct {
	fset    *token.FileSet
	name    string // Name of file.
	astFile *ast.File
	funcs   []*FuncExtent
}

func (v *FuncVisitor) Visit(node ast.Node) ast.Visitor {
	switch n := node.(type) {
	case *ast.FuncDecl:
		start := v.fset.Position(n.Pos())
		end := v.fset.Position(n.End())
		fe := &FuncExtent{
			name:      n.Name.Name,
			startLine: start.Line,
			startCol:  start.Column,
			endLine:   end.Line,
			endCol:    end.Column,
		}
		v.funcs = append(v.funcs, fe)
	}
	return v
}

type FuncExtent struct {
	name      string
	startLine int
	startCol  int
	endLine   int
	endCol    int
}

func (f *FuncExtent) coverage(profile *cover.Profile) (num, den int64) {
	// We could avoid making this n^2 overall by doing a single scan and annotating the functions,
	// but the sizes of the data structures is never very large and the scan is almost instantaneous.
	var covered, total int64
	// The blocks are sorted, so we can stop counting as soon as we reach the end of the relevant block.
	for _, b := range profile.Blocks {
		if b.StartLine > f.endLine || (b.StartLine == f.endLine && b.StartCol >= f.endCol) {
			// Past the end of the function.
			break
		}
		if b.EndLine < f.startLine || (b.EndLine == f.startLine && b.EndCol <= f.startCol) {
			// Before the beginning of the function
			continue
		}
		total += int64(b.NumStmt)
		if b.Count > 0 {
			covered += int64(b.NumStmt)
		}
	}
	if total == 0 {
		total = 1 // Avoid zero denominator.
	}
	return covered, total
}

type Pkg struct {
	ImportPath string
	Dir        string
	Error      *struct {
		Err string
	}
}

func findPkgs(profiles []*cover.Profile) (map[string]*Pkg, error) {
	// Run go list to find the location of every package we care about.
	pkgs := make(map[string]*Pkg)
	var list []string
	for _, profile := range profiles {
		if strings.HasPrefix(profile.FileName, ".") || filepath.IsAbs(profile.FileName) {
			// Relative or absolute path.
			continue
		}
		pkg := path.Dir(profile.FileName)
		if _, ok := pkgs[pkg]; !ok {
			pkgs[pkg] = nil
			list = append(list, pkg)
		}
	}

	if len(list) == 0 {
		return pkgs, nil
	}

	// Note: usually run as "go tool cover" in which case $GOROOT is set,
	// in which case runtime.GOROOT() does exactly what we want.
	goTool := filepath.Join(runtime.GOROOT(), "bin/go")
	cmd := exec.Command(goTool, append([]string{"list", "-e", "-json"}, list...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("cannot run go list: %v\n%s", err, stderr.Bytes())
	}
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for {
		var pkg Pkg
		err := dec.Decode(&pkg)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decoding go list json: %v", err)
		}
		pkgs[pkg.ImportPath] = &pkg
	}
	return pkgs, nil
}

func findFile(pkgs map[string]*Pkg, file string) (string, error) {
	if strings.HasPrefix(file, ".") || filepath.IsAbs(file) {
		// Relative or absolute path.
		return file, nil
	}
	pkg := pkgs[path.Dir(file)]
	if pkg != nil {
		if pkg.Dir != "" {
			return filepath.Join(pkg.Dir, path.Base(file)), nil
		}
		if pkg.Error != nil {
			return "", errors.New(pkg.Error.Err)
		}
	}
	return "", fmt.Errorf("did not find package for %s in go list output", file)
}
