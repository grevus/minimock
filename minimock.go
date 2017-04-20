package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/loader"

	"github.com/gojuno/generator"
)

type (
	options struct {
		InputFile     string
		OutputFile    string
		InterfaceName string
		StructName    string
		Package       string
	}

	visitor struct {
		gen             *generator.Generator
		methods         map[string]*types.Signature
		sourceInterface string
	}
)

func main() {
	opts := processFlags()
	var (
		packagePath = opts.InputFile
		err         error
	)

	if _, err := os.Stat(packagePath); err == nil {
		if packagePath, err = generator.PackageOf(packagePath); err != nil {
			die(err)
		}
	}

	destPackagePath, err := generator.PackageOf(filepath.Dir(opts.OutputFile))
	if err != nil {
		die(err)
	}

	cfg := loader.Config{
		AllowErrors:         true,
		TypeCheckFuncBodies: func(string) bool { return false },
		TypeChecker: types.Config{
			IgnoreFuncBodies:         true,
			FakeImportC:              true,
			DisableUnusedImportCheck: true,
			Error: func(err error) {},
		},
	}
	cfg.Import(packagePath)

	if err := os.Remove(opts.OutputFile); err != nil && !os.IsNotExist(err) {
		die(err)
	}

	if destPackagePath != packagePath {
		cfg.Import(destPackagePath)
	}

	prog, err := cfg.Load()
	if err != nil {
		die(err)
	}

	gen := generator.New(prog)
	gen.ImportWithAlias(destPackagePath, "")
	gen.SetPackageName(opts.Package)
	gen.SetVar("structName", opts.StructName)
	gen.SetVar("interfaceName", opts.InterfaceName)
	gen.SetHeader(fmt.Sprintf(`DO NOT EDIT!
This code was generated automatically using github.com/gojuno/minimock v1.0
Original interface %q can be found in %s`, opts.InterfaceName, packagePath))
	gen.SetDefaultParamsPrefix("p")
	gen.SetDefaultResultsPrefix("r")

	v := &visitor{
		gen:             gen,
		methods:         map[string]*types.Signature{},
		sourceInterface: opts.InterfaceName,
	}

	for _, file := range prog.Package(packagePath).Files {
		ast.Walk(v, file)
	}

	if len(v.methods) == 0 {
		die(fmt.Errorf("interface %s was not found in %s or it's an empty interface", opts.InterfaceName, packagePath))
	}

	if err := gen.ProcessTemplate("interface", template, v.methods); err != nil {
		die(err)
	}

	if err := gen.WriteToFilename(opts.OutputFile); err != nil {
		die(err)
	}
}

func (v *visitor) Visit(node ast.Node) ast.Visitor {
	switch ts := node.(type) {
	case *ast.FuncDecl:
		return nil
	case *ast.TypeSpec:
		exprType, err := v.gen.ExpressionType(ts.Type)
		if err != nil {
			die(fmt.Errorf("failed to get expression for %T %s", ts.Type, ts.Name.Name, err))
		}

		var i *types.Interface

		switch t := exprType.(type) {
		case *types.Named:
			underlying, ok := t.Underlying().(*types.Interface)
			if !ok {
				return nil
			}
			i = underlying
		case *types.Interface:
			i = t
		}

		if ts.Name.Name == v.sourceInterface {
			v.processInterface(i)
		}

		return nil
	}

	return v
}

func (v *visitor) processInterface(t *types.Interface) {
	for i := 0; i < t.NumMethods(); i++ {
		v.methods[t.Method(i).Name()] = t.Method(i).Type().(*types.Signature)
	}
}

const template = `
	type {{$structName}} struct {
		t *testing.T
		m *sync.RWMutex

		{{ range $methodName, $method := . }} {{$methodName}}Func func{{ signature $method }}
		{{ end }}
		{{ range $methodName, $method := . }} {{$methodName}}Counter int
		{{ end }}
		{{ range $methodName, $method := . }} {{$methodName}}Mock {{$structName}}{{$methodName}}
		{{ end }}
	}

	func New{{$structName}}(t *testing.T) *{{$structName}} {
		m := &{{$structName}}{t: t, m: &sync.RWMutex{} }
		{{ range $methodName, $method := . }}m.{{$methodName}}Mock = {{$structName}}{{$methodName}}{mock: m}
		{{ end }}

		return m
	}

	{{ range $methodName, $method := . }}
		type {{$structName}}{{$methodName}} struct {
			mock *{{$structName}}
		}

		func (m {{$structName}}{{$methodName}}) Return({{results $method}}) *{{$structName}} {
			m.mock.{{$methodName}}Func = func({{params $method}}) ({{(results $method).Types}}) {
				return {{ (results $method).Names }}
			}
			return m.mock
		}

		func (m {{$structName}}{{$methodName}}) Set(f func({{params $method}}) ({{results $method}})) *{{$structName}}{
			m.mock.{{$methodName}}Func = f
			return m.mock
		}

		func (m *{{$structName}}) {{$methodName}}{{signature $method}} {
			m.m.Lock()
			m.{{$methodName}}Counter += 1
			m.m.Unlock()

			if m.{{$methodName}}Func == nil {
				m.t.Fatal("Unexpected call to {{$structName}}.{{$methodName}}")
			}

			{{if gt (len (results $method)) 0 }}
			return {{ end }} m.{{$methodName}}Func({{(params $method).Pass}})
		}
	{{ end }}

	func (m *{{$structName}}) ValidateCallCounters() {
		m.t.Log("ValidateCallCounters is deprecated please use CheckMocksCalled")

		{{ range $methodName, $method := . }}
			if m.{{$methodName}}Func != nil && m.{{$methodName}}Counter == 0 {
				m.t.Fatal("Expected call to {{$structName}}.{{$methodName}}")
			}
		{{ end }}
	}

	func (m *{{$structName}}) CheckMocksCalled() {
		{{ range $methodName, $method := . }}
			if m.{{$methodName}}Func != nil && m.{{$methodName}}Counter == 0 {
				m.t.Fatal("Expected call to {{$structName}}.{{$methodName}}")
			}
		{{ end }}
	}

	//AllMocksCalled returns true if all mocked methods were called before the call to AllMocksCalled,
	//it can be used with assert/require, i.e. assert.True(mock.AllMocksCalled())
	func (m *{{$structName}}) AllMocksCalled() bool {
		m.m.RLock()
		defer m.m.RUnlock()

		{{ range $methodName, $method := . }}
			if m.{{$methodName}}Func != nil && m.{{$methodName}}Counter == 0 {
				return false
			}
		{{ end }}

		return true
	}`

func processFlags() *options {
	var (
		input  = flag.String("f", "", "input file or import path of the package containing interface declaration")
		name   = flag.String("i", "", "interface name")
		output = flag.String("o", "", "destination file for interface implementation")
		pkg    = flag.String("p", "", "destination package name")
		sname  = flag.String("t", "", "target struct name, default: <interface name>Mock")
	)

	flag.Parse()

	if *pkg == "" || *input == "" || *output == "" || *name == "" || !strings.HasSuffix(*output, ".go") {
		flag.Usage()
		os.Exit(1)
	}

	if *sname == "" {
		*sname = *name + "Mock"
	}

	return &options{
		InputFile:     *input,
		OutputFile:    *output,
		InterfaceName: *name,
		Package:       *pkg,
		StructName:    *sname,
	}
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "%v\n", err)
	os.Exit(1)
}
