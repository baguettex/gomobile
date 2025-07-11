// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"path"
	"strings"

	"github.com/baguettex/gomobile/internal/importers/objc"
)

type (
	// ObjCWrapper generates Go and C stubs for ObjC interfaces and protocols.
	ObjcWrapper struct {
		*Printer
		imported map[string]*objc.Named
		// The list of ObjC types.
		types []*objc.Named
		// The list of Go package paths with ObjC wrappers.
		pkgNames []string
		modules  []string
		// For each ObjC module, the list of ObjC types within.
		modMap map[string][]*objc.Named
		// For each module/name Go package path, the ObjC type
		// with static functions or constants.
		typePkgs map[string]*objc.Named
		// supers is the map of types that need Super methods.
		supers map[string]struct{}
	}
)

// Init initializes the ObjC types wrapper generator. Types is the
// list of types to wrap, genNames the list of generated type names.
func (g *ObjcWrapper) Init(types []*objc.Named, genNames []string) {
	g.supers = make(map[string]struct{})
	for _, s := range genNames {
		g.supers[s] = struct{}{}
	}
	g.types = types
	g.imported = make(map[string]*objc.Named)
	g.modMap = make(map[string][]*objc.Named)
	g.typePkgs = make(map[string]*objc.Named)
	pkgSet := make(map[string]struct{})
	for _, n := range types {
		g.imported[n.GoName] = n
		typePkg := n.Module + "/" + n.GoName
		g.typePkgs[typePkg] = n
		if !n.Generated {
			if _, exists := g.modMap[n.Module]; !exists {
				g.modules = append(g.modules, n.Module)
			}
		}
		g.modMap[n.Module] = append(g.modMap[n.Module], n)
		if _, exists := pkgSet[n.Module]; !exists {
			pkgSet[n.Module] = struct{}{}
			g.pkgNames = append(g.pkgNames, n.Module)
		}
		g.pkgNames = append(g.pkgNames, typePkg)
	}
}

func (g *ObjcWrapper) GenM() {
	g.Printf(gobindPreamble)
	// For objc_msgSend* functions.
	g.Printf("@import ObjectiveC.message;\n")
	g.Printf("#include \"seq.h\"\n")
	g.Printf("#include \"interfaces.h\"\n\n")
	for _, n := range g.types {
		g.genM(n)
	}
	g.Printf("\n")
	for _, n := range g.types {
		for _, f := range n.AllMethods {
			if !g.isFuncSupported(f) {
				continue
			}
			g.genCFuncDecl("cproxy", n.GoName, f)
			g.genCFuncBody(n, f, false)
			if _, exists := g.supers[n.GoName]; exists {
				g.genCFuncDecl("csuper", n.GoName, f)
				g.genCFuncBody(n, f, true)
			}
		}
	}
}

func (g *ObjcWrapper) genCFuncBody(n *objc.Named, f *objc.Func, super bool) {
	g.Printf(" {\n")
	g.Indent()
	if !f.Static {
		g.Printf("%s _this = go_seq_from_refnum(this).obj;\n", n.ObjcType())
	}
	var errParam *objc.Param
	for i, a := range f.Params {
		if i == len(f.Params)-1 && g.isErrorType(a.Type) {
			errParam = a
			break
		}
		g.genCToObjC(a.Name, a.Type, modeTransient)
	}
	if errParam != nil {
		g.Printf("NSError *%s = nil;\n", errParam.Name)
	}
	if f.Constructor {
		g.Printf("%s _this = [%s alloc];\n", n.ObjcType(), n.Name)
	}
	if super {
		g.Printf("struct objc_super _super = {\n")
		g.Printf("	.receiver = _this,\n")
		g.Printf("	.super_class = class_getSuperclass([%s class]),\n", n.Name)
		g.Printf("};\n")
	}
	retType := "void"
	if f.Ret != nil {
		retType = g.objcType(f.Ret)
		g.Printf("%s res = ", retType)
	}
	// There is no direct way to send a message to a class' super
	// class from outside the class itself. Use objc_msgSendSuper instead
	// which is what the compiler uses itself. To keep us honest and to exercise
	// the code paths more use objc_msgSend for regular calls as well.
	//
	// A regular call looks like this:
	//
	// res = ((<return type> (*)(id, SEL, <argument_types>))objc_msgSend)(_this, @selector(...), <arguments>)
	//
	// a call to super looks like this:
	//
	// ret = ((<return type> (*)(id, SEL, <argument_types>))objc_msgSendSuper)(<struct objc_super>, <arguments>)
	if f.Ret != nil {
		switch f.Ret.Kind {
		case objc.String, objc.Bool, objc.Data, objc.Int, objc.Uint, objc.Short, objc.Ushort, objc.Char, objc.Uchar, objc.Float, objc.Double, objc.Class, objc.Protocol:
		default:
			// If support for struct results is added, objc_msgSend_stret must be used
			panic("unsupported type kind - use objc_msgSend_stret?")
		}
	}
	g.Printf("((%s (*)(", retType)
	if super {
		g.Printf("struct objc_super *")
	} else {
		g.Printf("id")
	}
	g.Printf(", SEL")
	for _, a := range f.Params {
		g.Printf(", %s", g.objcType(a.Type))
	}
	g.Printf("))")
	if super {
		g.Printf("objc_msgSendSuper")
	} else {
		g.Printf("objc_msgSend")
	}
	g.Printf(")(")
	if f.Static && !f.Constructor {
		g.Printf("[%s class]", n.Name)
	} else {
		if super {
			g.Printf("&_super")
		} else {
			g.Printf("_this")
		}
	}
	g.Printf(", @selector(%s)", f.Sig)
	for _, a := range f.Params {
		arg := "_" + a.Name
		if a == errParam {
			arg = "&" + a.Name
		}
		g.Printf(", %s", arg)
	}
	g.Printf(");\n")
	if errParam != nil {
		g.Printf("NSError *_%s = nil;\n", errParam.Name)
		if f.Ret != nil {
			g.Printf("if (!res && %s != nil) {\n", errParam.Name)
		} else {
			g.Printf("if (%s != nil) {\n", errParam.Name)
		}
		g.Printf("	_%[1]s = %[1]s;\n", errParam.Name)
		g.Printf("}\n")
		g.genObjCToC("_"+errParam.Name, g.errType(), modeRetained)
	}
	ret := f.Ret
	if ret != nil && ret.Kind == objc.Bool && errParam != nil {
		ret = nil
	}
	if ret != nil {
		g.genObjCToC("res", ret, modeRetained)
	}
	switch {
	case ret != nil && errParam != nil:
		stype := strings.Replace(g.cType(ret), " ", "_", -1)
		g.Printf("ret_%s _sres = {_res, __%s};\n", stype, errParam.Name)
		g.Printf("return _sres;\n")
	case ret != nil:
		g.Printf("return _res;\n")
	case errParam != nil:
		g.Printf("return __%s;\n", errParam.Name)
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func (_ *ObjcWrapper) errType() *objc.Type {
	return &objc.Type{Kind: objc.Class, Name: "NSError"}
}

func (g *ObjcWrapper) genM(n *objc.Named) {
	for _, f := range n.Funcs {
		if !g.isFuncSupported(f) {
			continue
		}
		g.genCFuncDecl("cproxy", n.GoName, f)
		g.genCFuncBody(n, f, false)
	}
}

func (g *ObjcWrapper) GenH() {
	g.Printf(gobindPreamble)
	g.Printf("#include \"seq.h\"\n\n")
	for _, m := range g.modules {
		g.Printf("@import %s;\n", m)
	}
	// Include header files for generated types
	for _, n := range g.pkgNames {
		hasGen := false
		for _, t := range g.modMap[n] {
			if t.Generated {
				hasGen = true
				break
			}
		}
		if hasGen {
			g.Printf("#import %q\n", n+".objc.h")
		}
	}
	for _, tn := range []string{"int", "nstring", "nbyteslice", "long", "unsigned long", "short", "unsigned short", "bool", "char", "unsigned char", "float", "double"} {
		sn := strings.Replace(tn, " ", "_", -1)
		g.Printf("typedef struct ret_%s {\n", sn)
		g.Printf("	%s res;\n", tn)
		g.Printf("	int err;\n")
		g.Printf("} ret_%s;\n", sn)
	}
	g.Printf("\n")
	for _, n := range g.types {
		for _, f := range n.AllMethods {
			if !g.isFuncSupported(f) {
				continue
			}
			g.Printf("extern ")
			g.genCFuncDecl("cproxy", n.GoName, f)
			g.Printf(";\n")
			if _, exists := g.supers[n.GoName]; exists {
				g.Printf("extern ")
				g.genCFuncDecl("csuper", n.GoName, f)
				g.Printf(";\n")
			}
		}
	}
	for _, cls := range g.types {
		g.genH(cls)
	}
}

func (g *ObjcWrapper) genH(n *objc.Named) {
	for _, f := range n.Funcs {
		if !g.isFuncSupported(f) {
			continue
		}
		g.Printf("extern ")
		g.genCFuncDecl("cproxy", n.GoName, f)
		g.Printf(";\n")
	}
}

func (g *ObjcWrapper) genCFuncDecl(prefix, name string, f *objc.Func) {
	returnsErr := len(f.Params) > 0 && g.isErrorType(f.Params[len(f.Params)-1].Type)
	ret := f.Ret
	if ret != nil && returnsErr && ret.Kind == objc.Bool {
		ret = nil
	}
	switch {
	case ret != nil && returnsErr:
		g.Printf("ret_%s", strings.Replace(g.cType(ret), " ", "_", -1))
	case ret != nil:
		g.Printf(g.cType(ret))
	case returnsErr:
		g.Printf("int")
	default:
		g.Printf("void")
	}
	g.Printf(" ")
	g.Printf(prefix)
	if f.Static {
		g.Printf("_s")
	}
	g.Printf("_%s_%s(", name, f.GoName)
	if !f.Static {
		g.Printf("int this")
	}
	for i, p := range f.Params {
		if i == len(f.Params)-1 && returnsErr {
			break
		}
		if !f.Static || i > 0 {
			g.Printf(", ")
		}
		g.Printf("%s %s", g.cType(p.Type), p.Name)
	}
	g.Printf(")")
}

func (g *ObjcWrapper) GenGo() {
	g.Printf(gobindPreamble)
	g.Printf("package main\n\n")
	g.Printf("// #include \"interfaces.h\"\n")
	g.Printf("import \"C\"\n\n")
	g.Printf("import \"ObjC\"\n")
	g.Printf("import _seq \"github.com/baguettex/gomobile/bind/seq\"\n")

	for _, n := range g.types {
		for _, f := range n.Funcs {
			if g.isFuncSupported(f) {
				pkgName := n.Module + "/" + n.GoName
				g.Printf("import %q\n", "ObjC/"+pkgName)
				break
			}
		}
	}
	g.Printf("\n")
	g.Printf("type proxy interface { Bind_proxy_refnum__() int32 }\n\n")
	g.Printf("// Suppress unused package error\n\n")
	g.Printf("var _ = _seq.FromRefNum\n")
	g.Printf("const _ = ObjC.Dummy\n\n")
	for _, n := range g.types {
		g.genGo(n)
	}
}

func (g *ObjcWrapper) genGo(n *objc.Named) {
	g.Printf("func init() {\n")
	g.Indent()
	for _, f := range n.Funcs {
		if !g.isFuncSupported(f) {
			continue
		}
		g.Printf("%s.%s = func", n.GoName, f.GoName)
		g.genFuncDecl(false, f)
		g.genFuncBody(n, f, "cproxy")
	}
	g.Outdent()
	g.Printf("}\n\n")
	g.Printf("type proxy_class_%s _seq.Ref\n\n", n.GoName)
	g.Printf("func (p *proxy_class_%s) Bind_proxy_refnum__() int32 { return (*_seq.Ref)(p).Bind_IncNum() }\n\n", n.GoName)
	for _, f := range n.AllMethods {
		if !g.isFuncSupported(f) {
			continue
		}
		g.Printf("func (p *proxy_class_%s) %s", n.GoName, f.GoName)
		g.genFuncDecl(false, f)
		g.genFuncBody(n, f, "cproxy")
	}
	if _, exists := g.supers[n.GoName]; exists {
		g.Printf("func (p *proxy_class_%s) Super() ObjC.%s {\n", n.GoName, n.Module+"_"+n.GoName)
		g.Printf("  return &super_%s{p}\n", n.GoName)
		g.Printf("}\n\n")
		g.Printf("type super_%s struct {*proxy_class_%[1]s}\n\n", n.GoName)
		for _, f := range n.AllMethods {
			if !g.isFuncSupported(f) {
				continue
			}
			g.Printf("func (p *super_%s) %s", n.GoName, f.GoName)
			g.genFuncDecl(false, f)
			g.genFuncBody(n, f, "csuper")
		}
	}
}

func (g *ObjcWrapper) genFuncBody(n *objc.Named, f *objc.Func, prefix string) {
	g.Printf(" {\n")
	g.Indent()
	var errParam *objc.Param
	for i, a := range f.Params {
		if i == len(f.Params)-1 && g.isErrorType(a.Type) {
			errParam = a
			break
		}
		g.genWrite(a)
	}
	ret := f.Ret
	if ret != nil && errParam != nil && ret.Kind == objc.Bool {
		ret = nil
	}
	if ret != nil || errParam != nil {
		g.Printf("res := ")
	}
	g.Printf("C.")
	g.Printf(prefix)
	if f.Static {
		g.Printf("_s")
	}
	g.Printf("_%s_%s(", n.GoName, f.GoName)
	if !f.Static {
		g.Printf("C.int(p.Bind_proxy_refnum__())")
	}
	for i, a := range f.Params {
		if a == errParam {
			break
		}
		if !f.Static || i > 0 {
			g.Printf(", ")
		}
		g.Printf("_%s", a.Name)
	}
	g.Printf(")\n")
	switch {
	case ret != nil && errParam != nil:
		g.genRead("_res", "res.res", ret)
		g.genRefRead("_"+errParam.Name, "res.err", "error", "proxy_error")
		g.Printf("return _res, _%s\n", errParam.Name)
	case ret != nil:
		g.genRead("_res", "res", ret)
		g.Printf("return _res\n")
	case errParam != nil:
		g.genRefRead("_"+errParam.Name, "res", "error", "proxy_error")
		g.Printf("return _%s\n", errParam.Name)
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *ObjcWrapper) genCToObjC(name string, t *objc.Type, mode varMode) {
	switch t.Kind {
	case objc.String:
		g.Printf("NSString *_%s = go_seq_to_objc_string(%s);\n", name, name)
	case objc.Bool:
		g.Printf("BOOL _%s = %s ? YES : NO;\n", name, name)
	case objc.Data:
		g.Printf("NSData *_%s = go_seq_to_objc_bytearray(%s, %d);\n", name, name, toCFlag(mode == modeRetained))
	case objc.Int, objc.Uint, objc.Short, objc.Ushort, objc.Char, objc.Uchar, objc.Float, objc.Double:
		g.Printf("%s _%s = (%s)%s;\n", g.objcType(t), name, g.objcType(t), name)
	case objc.Class, objc.Protocol:
		g.Printf("GoSeqRef* %s_ref = go_seq_from_refnum(%s);\n", name, name)
		g.Printf("%s _%s;\n", g.objcType(t), name)
		g.Printf("if (%s_ref != NULL) {\n", name)
		g.Printf("	_%s = %s_ref.obj;\n", name, name)
		g.Printf("}\n")
	default:
		panic("invalid kind")
	}
}

func (g *ObjcWrapper) genObjCToC(name string, t *objc.Type, mode varMode) {
	switch t.Kind {
	case objc.String:
		g.Printf("nstring _%s = go_seq_from_objc_string(%s);\n", name, name)
	case objc.Data:
		g.Printf("nbyteslice _%s = go_seq_from_objc_bytearray(%s, %d);\n", name, name, toCFlag(mode == modeRetained))
	case objc.Bool, objc.Int, objc.Uint, objc.Short, objc.Ushort, objc.Char, objc.Uchar, objc.Float, objc.Double:
		g.Printf("%s _%s = (%s)%s;\n", g.cType(t), name, g.cType(t), name)
	case objc.Protocol, objc.Class:
		g.Printf("int _%s = go_seq_to_refnum(%s);\n", name, name)
	default:
		panic("invalid kind")
	}
}

func (g *ObjcWrapper) genWrite(a *objc.Param) {
	switch a.Type.Kind {
	case objc.String:
		g.Printf("_%s := encodeString(%s)\n", a.Name, a.Name)
	case objc.Data:
		g.Printf("_%s := fromSlice(%s, false)\n", a.Name, a.Name)
	case objc.Bool:
		g.Printf("_%s := %s(0)\n", a.Name, g.cgoType(a.Type))
		g.Printf("if %s {\n", a.Name)
		g.Printf("  _%s = %s(1)\n", a.Name, g.cgoType(a.Type))
		g.Printf("}\n")
	case objc.Int, objc.Uint, objc.Short, objc.Ushort, objc.Char, objc.Uchar, objc.Float, objc.Double:
		g.Printf("_%s := %s(%s)\n", a.Name, g.cgoType(a.Type), a.Name)
	case objc.Protocol, objc.Class:
		g.Printf("var _%s %s = _seq.NullRefNum\n", a.Name, g.cgoType(a.Type))
		g.Printf("if %s != nil {\n", a.Name)
		g.Printf("  _%s = %s(_seq.ToRefNum(%s))\n", a.Name, g.cgoType(a.Type), a.Name)
		g.Printf("}\n")
	default:
		panic("invalid kind")
	}
}

func (g *ObjcWrapper) genRead(to, from string, t *objc.Type) {
	switch t.Kind {
	case objc.Int, objc.Uint, objc.Uchar, objc.Short, objc.Ushort, objc.Char, objc.Float, objc.Double:
		g.Printf("%s := %s(%s)\n", to, g.goType(t, false), from)
	case objc.Bool:
		g.Printf("%s := %s != 0\n", to, from)
	case objc.String:
		g.Printf("%s := decodeString(%s)\n", to, from)
	case objc.Data:
		g.Printf("%s := toSlice(%s, true)\n", to, from)
	case objc.Protocol, objc.Class:
		var proxyName string
		if n := g.lookupImported(t); n != nil {
			proxyName = "proxy_class_" + n.GoName
		}
		g.genRefRead(to, from, g.goType(t, false), proxyName)
	default:
		panic("invalid kind")
	}
}

func (g *ObjcWrapper) genRefRead(to, from string, intfName, proxyName string) {
	g.Printf("var %s %s\n", to, intfName)
	g.Printf("%s_ref := _seq.FromRefNum(int32(%s))\n", to, from)
	g.Printf("if %s_ref != nil {\n", to)
	g.Printf("	if %s < 0 { // go object\n", from)
	g.Printf("		%s = %s_ref.Get().(%s)\n", to, to, intfName)
	if proxyName != "" {
		g.Printf("	} else { // foreign object\n")
		g.Printf("		%s = (*%s)(%s_ref)\n", to, proxyName, to)
	}
	g.Printf("	}\n")
	g.Printf("}\n")
}

// Packages return the list of Go packages to be generated.
func (g *ObjcWrapper) Packages() []string {
	return g.pkgNames
}

func (g *ObjcWrapper) GenPackage(idx int) {
	pkg := g.pkgNames[idx]
	g.Printf(gobindPreamble)
	g.Printf("package %s\n\n", path.Base(pkg))
	g.Printf("import \"ObjC\"\n\n")
	g.Printf("const _ = ObjC.Dummy\n\n")
	for _, n := range g.modMap[pkg] {
		g.Printf("type %s ObjC.%s\n", n.GoName, n.Module+"_"+n.GoName)
	}
	if n, ok := g.typePkgs[pkg]; ok {
		g.Printf("var (\n")
		g.Indent()
		// Functions
		for _, f := range n.Funcs {
			if !g.isFuncSupported(f) {
				continue
			}
			g.Printf("%s func", f.GoName)
			g.genFuncDecl(false, f)
			g.Printf("\n")
		}
		g.Outdent()
		g.Printf(")\n\n")
	}
}

func (g *ObjcWrapper) GenInterfaces() {
	g.Printf(gobindPreamble)
	g.Printf("package ObjC\n\n")
	g.Printf("// Used to silence this package not used errors\n")
	g.Printf("const Dummy = 0\n\n")
	for _, n := range g.types {
		g.genInterface(n)
	}
}

func (g *ObjcWrapper) genInterface(n *objc.Named) {
	g.Printf("type %s interface {\n", n.Module+"_"+n.GoName)
	g.Indent()
	// Methods
	for _, f := range n.AllMethods {
		if !g.isFuncSupported(f) {
			continue
		}
		g.Printf(f.GoName)
		g.genFuncDecl(true, f)
		g.Printf("\n")
	}
	if _, exists := g.supers[n.GoName]; exists {
		g.Printf("Super() %s\n", n.Module+"_"+n.GoName)
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *ObjcWrapper) genFuncDecl(local bool, f *objc.Func) {
	var returnsErr bool
	g.Printf("(")
	for i, p := range f.Params {
		if i == len(f.Params)-1 && g.isErrorType(p.Type) {
			returnsErr = true
			break
		}
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("%s %s", p.Name, g.goType(p.Type, local))
	}
	g.Printf(")")
	if f.Ret != nil || returnsErr {
		ret := f.Ret
		if ret.Kind == objc.Bool && returnsErr {
			// Skip the bool result and use the error results.
			ret = nil
		}
		if ret != nil {
			g.Printf(" (%s", g.goType(f.Ret, local))
			if returnsErr {
				g.Printf(", error")
			}
			g.Printf(")")
		} else {
			g.Printf(" error")
		}
	}
}

func (g *ObjcWrapper) isFuncSupported(f *objc.Func) bool {
	for i, p := range f.Params {
		if !g.isSupported(p.Type) {
			if i < len(f.Params)-1 || !g.isErrorType(p.Type) {
				return false
			}
		}
	}
	if f.Ret != nil {
		return g.isSupported(f.Ret)
	}
	return true
}

func (g *ObjcWrapper) isErrorType(t *objc.Type) bool {
	// Must be a NSError ** type
	return t.Kind == objc.Class && t.Indirect && t.Name == "NSError"
}

func (g *ObjcWrapper) isSupported(t *objc.Type) bool {
	if t.Indirect {
		return false
	}
	switch t.Kind {
	case objc.Unknown:
		return false
	case objc.Protocol:
		// TODO: support inout parameters.
		return !strings.HasSuffix(t.Decl, " *")
	case objc.Class:
		return t.Name != "SEL" && t.Name != "void"
	default:
		return true
	}
}

func (g *ObjcWrapper) cgoType(t *objc.Type) string {
	switch t.Kind {
	case objc.Uint:
		return "C.ulong"
	case objc.Ushort:
		return "C.ushort"
	case objc.Uchar:
		return "C.uchar"
	default:
		return "C." + g.cType(t)
	}
}

func (g *ObjcWrapper) cType(t *objc.Type) string {
	switch t.Kind {
	case objc.Protocol, objc.Class:
		return "int"
	case objc.String:
		return "nstring"
	case objc.Data:
		return "nbyteslice"
	case objc.Int:
		return "long"
	case objc.Uint:
		return "unsigned long"
	case objc.Short:
		return "short"
	case objc.Ushort:
		return "unsigned short"
	case objc.Bool:
		return "char"
	case objc.Char:
		return "char"
	case objc.Uchar:
		return "unsigned char"
	case objc.Float:
		return "float"
	case objc.Double:
		return "double"
	default:
		panic("invalid kind")
	}
}

func (g *ObjcWrapper) objcType(t *objc.Type) string {
	return t.Decl
}

func (g *ObjcWrapper) lookupImported(t *objc.Type) *objc.Named {
	var mangled string
	switch t.Kind {
	case objc.Class:
		mangled = t.Name + "C"
	case objc.Protocol:
		mangled = t.Name + "P"
	default:
		panic("invalid type kind")
	}
	if n, exists := g.imported[mangled]; exists {
		return n
	}
	return g.imported[t.Name]
}

func (g *ObjcWrapper) goType(t *objc.Type, local bool) string {
	switch t.Kind {
	case objc.String:
		return "string"
	case objc.Data:
		return "[]byte"
	case objc.Int:
		return "int"
	case objc.Uint:
		return "uint"
	case objc.Short:
		return "int16"
	case objc.Ushort:
		return "uint16"
	case objc.Bool:
		return "bool"
	case objc.Char:
		return "byte"
	case objc.Uchar:
		return "uint8"
	case objc.Float:
		return "float32"
	case objc.Double:
		return "float64"
	case objc.Protocol, objc.Class:
		var n *objc.Named
		n = g.lookupImported(t)
		name := n.Module + "_" + n.GoName
		if !local {
			name = "ObjC." + name
		}
		return name
	default:
		panic("invalid kind")
	}
}
