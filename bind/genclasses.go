// Copyright 2016 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"path"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/baguettex/gomobile/internal/importers"
	"github.com/baguettex/gomobile/internal/importers/java"
)

type (
	// ClassGen generates Go and C stubs for Java classes so import statements
	// on the form
	//
	//
	// import "Java/classpath/to/Class"
	//
	// will work.
	ClassGen struct {
		*Printer
		// JavaPkg is the Java package prefix for the generated classes. The prefix is prepended to the Go
		// package name to create the full Java package name.
		JavaPkg  string
		imported map[string]struct{}
		// The list of imported Java classes
		classes []*java.Class
		// The list of Go package paths with Java interfaces inside
		jpkgs []string
		// For each Go package path, the list of Java classes.
		typePkgs map[string][]*java.Class
		// For each Go package path, the Java class with static functions
		// or constants.
		clsPkgs map[string]*java.Class
		// goClsMap is the map of Java class names to Go type names, qualified with package name. Go types
		// that implement Java classes need Super methods and Unwrap methods.
		goClsMap map[string]string
		// goClsImports is the list of imports of user packages that contains the Go types implementing Java
		// classes.
		goClsImports []string
	}
)

func (g *ClassGen) isSupported(t *java.Type) bool {
	switch t.Kind {
	case java.Array:
		// TODO: Support all array types
		return t.Elem.Kind == java.Byte
	default:
		return true
	}
}

func (g *ClassGen) isFuncSetSupported(fs *java.FuncSet) bool {
	for _, f := range fs.Funcs {
		if g.isFuncSupported(f) {
			return true
		}
	}
	return false
}

func (g *ClassGen) isFuncSupported(f *java.Func) bool {
	for _, a := range f.Params {
		if !g.isSupported(a) {
			return false
		}
	}
	if f.Ret != nil {
		return g.isSupported(f.Ret)
	}
	return true
}

func (g *ClassGen) goType(t *java.Type, local bool) string {
	if t == nil {
		// interface{} is used for parameters types for overloaded methods
		// where no common ancestor type exists.
		return "interface{}"
	}
	switch t.Kind {
	case java.Int:
		return "int32"
	case java.Boolean:
		return "bool"
	case java.Short:
		return "int16"
	case java.Char:
		return "uint16"
	case java.Byte:
		return "byte"
	case java.Long:
		return "int64"
	case java.Float:
		return "float32"
	case java.Double:
		return "float64"
	case java.String:
		return "string"
	case java.Array:
		return "[]" + g.goType(t.Elem, local)
	case java.Object:
		name := goClsName(t.Class)
		if !local {
			name = "Java." + name
		}
		return name
	default:
		panic("invalid kind")
	}
}

// Init initializes the class wrapper generator. Classes is the
// list of classes to wrap, goClasses is the list of Java classes
// implemented in Go.
func (g *ClassGen) Init(classes []*java.Class, goClasses []importers.Struct) {
	g.goClsMap = make(map[string]string)
	impMap := make(map[string]struct{})
	for _, s := range goClasses {
		n := s.Pkg + "." + s.Name
		jn := n
		if g.JavaPkg != "" {
			jn = g.JavaPkg + "." + jn
		}
		g.goClsMap[jn] = n
		if _, exists := impMap[s.PkgPath]; !exists {
			impMap[s.PkgPath] = struct{}{}
			g.goClsImports = append(g.goClsImports, s.PkgPath)
		}
	}
	g.classes = classes
	g.imported = make(map[string]struct{})
	g.typePkgs = make(map[string][]*java.Class)
	g.clsPkgs = make(map[string]*java.Class)
	pkgSet := make(map[string]struct{})
	for _, cls := range classes {
		g.imported[cls.Name] = struct{}{}
		clsPkg := strings.Replace(cls.Name, ".", "/", -1)
		g.clsPkgs[clsPkg] = cls
		typePkg := path.Dir(clsPkg)
		g.typePkgs[typePkg] = append(g.typePkgs[typePkg], cls)
		if _, exists := pkgSet[clsPkg]; !exists {
			pkgSet[clsPkg] = struct{}{}
			g.jpkgs = append(g.jpkgs, clsPkg)
		}
		if _, exists := pkgSet[typePkg]; !exists {
			pkgSet[typePkg] = struct{}{}
			g.jpkgs = append(g.jpkgs, typePkg)
		}
	}
}

// Packages return the list of Go packages to be generated.
func (g *ClassGen) Packages() []string {
	return g.jpkgs
}

func (g *ClassGen) GenPackage(idx int) {
	jpkg := g.jpkgs[idx]
	g.Printf(gobindPreamble)
	g.Printf("package %s\n\n", path.Base(jpkg))
	g.Printf("import \"Java\"\n\n")
	g.Printf("const _ = Java.Dummy\n\n")
	for _, cls := range g.typePkgs[jpkg] {
		g.Printf("type %s Java.%s\n", cls.PkgName, goClsName(cls.Name))
	}
	if cls, ok := g.clsPkgs[jpkg]; ok {
		g.Printf("const (\n")
		g.Indent()
		// Constants
		for _, v := range cls.Vars {
			if g.isSupported(v.Type) && v.Constant() {
				g.Printf("%s = %s\n", initialUpper(v.Name), v.Val)
			}
		}
		g.Outdent()
		g.Printf(")\n\n")

		g.Printf("var (\n")
		g.Indent()
		// Functions
	loop:
		for _, fs := range cls.Funcs {
			for _, f := range fs.Funcs {
				if f.Public && g.isFuncSupported(f) {
					g.Printf("%s func", fs.GoName)
					g.genFuncDecl(false, fs)
					g.Printf("\n")
					continue loop
				}
			}
		}
		g.Printf("// Cast takes a proxy for a Java object and converts it to a %s proxy.\n", cls.Name)
		g.Printf("// Cast panics if the argument is not a proxy or if the underlying object does\n")
		g.Printf("// not extend or implement %s.\n", cls.Name)
		g.Printf("Cast func(v interface{}) Java.%s\n", goClsName(cls.Name))
		g.Outdent()
		g.Printf(")\n\n")
	}
}

func (g *ClassGen) GenGo() {
	g.Printf(classesGoHeader)
	for _, cls := range g.classes {
		pkgName := strings.Replace(cls.Name, ".", "/", -1)
		g.Printf("import %q\n", "Java/"+pkgName)
	}
	for _, imp := range g.goClsImports {
		g.Printf("import %q\n", imp)
	}
	if len(g.classes) > 0 {
		g.Printf("import \"unsafe\"\n\n")
		g.Printf("import \"reflect\"\n\n")
		g.Printf("import \"fmt\"\n\n")
	}
	g.Printf("type proxy interface { Bind_proxy_refnum__() int32 }\n\n")
	g.Printf("// Suppress unused package error\n\n")
	g.Printf("var _ = _seq.FromRefNum\n")
	g.Printf("const _ = Java.Dummy\n\n")
	g.Printf("//export initClasses\n")
	g.Printf("func initClasses() {\n")
	g.Indent()
	g.Printf("C.init_proxies()\n")
	for _, cls := range g.classes {
		g.Printf("init_%s()\n", cls.JNIName)
	}
	g.Outdent()
	g.Printf("}\n\n")
	for _, cls := range g.classes {
		g.genGo(cls)
	}
}

func (g *ClassGen) GenH() {
	g.Printf(classesHHeader)
	for _, tn := range []string{"jint", "jboolean", "jshort", "jchar", "jbyte", "jlong", "jfloat", "jdouble", "nstring", "nbyteslice"} {
		g.Printf("typedef struct ret_%s {\n", tn)
		g.Printf("	%s res;\n", tn)
		g.Printf("	jint exc;\n")
		g.Printf("} ret_%s;\n", tn)
	}
	g.Printf("\n")
	for _, cls := range g.classes {
		for _, fs := range cls.AllMethods {
			for _, f := range fs.Funcs {
				if !g.isFuncSupported(f) {
					continue
				}
				g.Printf("extern ")
				g.genCMethodDecl("cproxy", cls.JNIName, f)
				g.Printf(";\n")
				if _, ok := g.goClsMap[cls.Name]; ok {
					g.Printf("extern ")
					g.genCMethodDecl("csuper", cls.JNIName, f)
					g.Printf(";\n")
				}
			}
		}
	}
	for _, cls := range g.classes {
		g.genH(cls)
	}
}

func (g *ClassGen) GenC() {
	g.Printf(classesCHeader)
	for _, cls := range g.classes {
		g.Printf("static jclass class_%s;\n", cls.JNIName)
		if _, ok := g.goClsMap[cls.Name]; ok {
			g.Printf("static jclass sclass_%s;\n", cls.JNIName)
		}
		for _, fs := range cls.Funcs {
			for _, f := range fs.Funcs {
				if !f.Public || !g.isFuncSupported(f) {
					continue
				}
				g.Printf("static jmethodID m_s_%s_%s;\n", cls.JNIName, f.JNIName)
			}
		}
		for _, fs := range cls.AllMethods {
			for _, f := range fs.Funcs {
				if g.isFuncSupported(f) {
					g.Printf("static jmethodID m_%s_%s;\n", cls.JNIName, f.JNIName)
					if _, ok := g.goClsMap[cls.Name]; ok {
						g.Printf("static jmethodID sm_%s_%s;\n", cls.JNIName, f.JNIName)
					}
				}
			}
		}
		g.genC(cls)
	}
	g.Printf("\n")
	g.Printf("void init_proxies() {\n")
	g.Indent()
	g.Printf("JNIEnv *env = go_seq_push_local_frame(%d);\n", len(g.classes))
	g.Printf("jclass clazz;\n")
	for _, cls := range g.classes {
		g.Printf("clazz = go_seq_find_class(%q);\n", strings.Replace(cls.FindName, ".", "/", -1))
		g.Printf("if (clazz != NULL) {\n")
		g.Indent()
		g.Printf("class_%s = (*env)->NewGlobalRef(env, clazz);\n", cls.JNIName)
		if _, ok := g.goClsMap[cls.Name]; ok {
			g.Printf("sclass_%s = (*env)->GetSuperclass(env, clazz);\n", cls.JNIName)
			g.Printf("sclass_%s = (*env)->NewGlobalRef(env, sclass_%s);\n", cls.JNIName, cls.JNIName)
		}
		for _, fs := range cls.Funcs {
			for _, f := range fs.Funcs {
				if !f.Public || !g.isFuncSupported(f) {
					continue
				}
				g.Printf("m_s_%s_%s = ", cls.JNIName, f.JNIName)
				if f.Constructor {
					g.Printf("go_seq_get_method_id(clazz, \"<init>\", %q);\n", f.Desc)
				} else {
					g.Printf("go_seq_get_static_method_id(clazz, %q, %q);\n", f.Name, f.Desc)
				}
			}
		}
		for _, fs := range cls.AllMethods {
			for _, f := range fs.Funcs {
				if g.isFuncSupported(f) {
					g.Printf("m_%s_%s = go_seq_get_method_id(clazz, %q, %q);\n", cls.JNIName, f.JNIName, f.Name, f.Desc)
					if _, ok := g.goClsMap[cls.Name]; ok {
						g.Printf("sm_%s_%s = go_seq_get_method_id(sclass_%s, %q, %q);\n", cls.JNIName, f.JNIName, cls.JNIName, f.Name, f.Desc)
					}
				}
			}
		}
		g.Outdent()
		g.Printf("}\n")
	}
	g.Printf("go_seq_pop_local_frame(env);\n")
	g.Outdent()
	g.Printf("}\n\n")
	for _, cls := range g.classes {
		for _, fs := range cls.AllMethods {
			for _, f := range fs.Funcs {
				if !g.isFuncSupported(f) {
					continue
				}
				g.genCMethodDecl("cproxy", cls.JNIName, f)
				g.genCMethodBody(cls, f, false)
				if _, ok := g.goClsMap[cls.Name]; ok {
					g.genCMethodDecl("csuper", cls.JNIName, f)
					g.genCMethodBody(cls, f, true)
				}
			}
		}
	}
}

func (g *ClassGen) GenInterfaces() {
	g.Printf(classesPkgHeader)
	for _, cls := range g.classes {
		g.genInterface(cls)
	}
}

func (g *ClassGen) genCMethodBody(cls *java.Class, f *java.Func, virtual bool) {
	g.Printf(" {\n")
	g.Indent()
	// Add 1 for the 'this' argument
	g.Printf("JNIEnv *env = go_seq_push_local_frame(%d);\n", len(f.Params)+1)
	g.Printf("// Must be a Java object\n")
	g.Printf("jobject _this = go_seq_from_refnum(env, this, NULL, NULL);\n")
	for i, a := range f.Params {
		g.genCToJava(fmt.Sprintf("a%d", i), a)
	}
	if f.Ret != nil {
		g.Printf("%s res = ", f.Ret.JNIType())
	}
	g.Printf("(*env)->Call")
	if virtual {
		g.Printf("Nonvirtual")
	}
	if f.Ret != nil {
		g.Printf(f.Ret.JNICallType())
	} else {
		g.Printf("Void")
	}
	g.Printf("Method(env, _this, ")
	if virtual {
		g.Printf("sclass_%s, sm_%s_%s", cls.JNIName, cls.JNIName, f.JNIName)
	} else {
		g.Printf("m_%s_%s", cls.JNIName, f.JNIName)
	}
	for i := range f.Params {
		g.Printf(", _a%d", i)
	}
	g.Printf(");\n")
	g.Printf("jobject _exc = go_seq_get_exception(env);\n")
	g.Printf("int32_t _exc_ref = go_seq_to_refnum(env, _exc);\n")
	if f.Ret != nil {
		g.genCRetClear("res", f.Ret, "_exc")
		g.genJavaToC("res", f.Ret)
	}
	g.Printf("go_seq_pop_local_frame(env);\n")
	if f.Ret != nil {
		g.Printf("ret_%s __res = {_res, _exc_ref};\n", f.Ret.CType())
		g.Printf("return __res;\n")
	} else {
		g.Printf("return _exc_ref;\n")
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func initialUpper(s string) string {
	if s == "" {
		return ""
	}
	r, n := utf8.DecodeRuneInString(s)
	return string(unicode.ToUpper(r)) + s[n:]
}

func (g *ClassGen) genFuncDecl(local bool, fs *java.FuncSet) {
	g.Printf("(")
	for i, a := range fs.Params {
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("a%d ", i)
		if i == len(fs.Params)-1 && fs.Variadic {
			g.Printf("...")
		}
		g.Printf(g.goType(a, local))
	}
	g.Printf(")")
	if fs.Throws {
		if fs.HasRet {
			g.Printf(" (%s, error)", g.goType(fs.Ret, local))
		} else {
			g.Printf(" error")
		}
	} else if fs.HasRet {
		g.Printf(" %s", g.goType(fs.Ret, local))
	}
}

func (g *ClassGen) genC(cls *java.Class) {
	for _, fs := range cls.Funcs {
		for _, f := range fs.Funcs {
			if !f.Public || !g.isFuncSupported(f) {
				continue
			}
			g.genCFuncDecl(cls.JNIName, f)
			g.Printf(" {\n")
			g.Indent()
			g.Printf("JNIEnv *env = go_seq_push_local_frame(%d);\n", len(f.Params))
			for i, a := range f.Params {
				g.genCToJava(fmt.Sprintf("a%d", i), a)
			}
			if f.Constructor {
				g.Printf("jobject res = (*env)->NewObject(env")
			} else if f.Ret != nil {
				g.Printf("%s res = (*env)->CallStatic%sMethod(env", f.Ret.JNIType(), f.Ret.JNICallType())
			} else {
				g.Printf("(*env)->CallStaticVoidMethod(env")
			}
			g.Printf(", class_%s, m_s_%s_%s", cls.JNIName, cls.JNIName, f.JNIName)
			for i := range f.Params {
				g.Printf(", _a%d", i)
			}
			g.Printf(");\n")
			g.Printf("jobject _exc = go_seq_get_exception(env);\n")
			g.Printf("int32_t _exc_ref = go_seq_to_refnum(env, _exc);\n")
			if f.Ret != nil {
				g.genCRetClear("res", f.Ret, "_exc")
				g.genJavaToC("res", f.Ret)
			}
			g.Printf("go_seq_pop_local_frame(env);\n")
			if f.Ret != nil {
				g.Printf("ret_%s __res = {_res, _exc_ref};\n", f.Ret.CType())
				g.Printf("return __res;\n")
			} else {
				g.Printf("return _exc_ref;\n")
			}
			g.Outdent()
			g.Printf("}\n\n")
		}
	}
}

func (g *ClassGen) genH(cls *java.Class) {
	for _, fs := range cls.Funcs {
		for _, f := range fs.Funcs {
			if !f.Public || !g.isFuncSupported(f) {
				continue
			}
			g.Printf("extern ")
			g.genCFuncDecl(cls.JNIName, f)
			g.Printf(";\n")
		}
	}
}

func (g *ClassGen) genCMethodDecl(prefix, jniName string, f *java.Func) {
	if f.Ret != nil {
		g.Printf("ret_%s", f.Ret.CType())
	} else {
		// Return only the exception, if any
		g.Printf("jint")
	}
	g.Printf(" %s_%s_%s(jint this", prefix, jniName, f.JNIName)
	for i, a := range f.Params {
		g.Printf(", %s a%d", a.CType(), i)
	}
	g.Printf(")")
}

func (g *ClassGen) genCFuncDecl(jniName string, f *java.Func) {
	if f.Ret != nil {
		g.Printf("ret_%s", f.Ret.CType())
	} else {
		// Return only the exception, if any
		g.Printf("jint")
	}
	g.Printf(" cproxy_s_%s_%s(", jniName, f.JNIName)
	for i, a := range f.Params {
		if i > 0 {
			g.Printf(", ")
		}
		g.Printf("%s a%d", a.CType(), i)
	}
	g.Printf(")")
}

func (g *ClassGen) genGo(cls *java.Class) {
	g.Printf("var class_%s C.jclass\n\n", cls.JNIName)
	g.Printf("func init_%s() {\n", cls.JNIName)
	g.Indent()
	g.Printf("cls := C.CString(%q)\n", strings.Replace(cls.FindName, ".", "/", -1))
	g.Printf("clazz := C.go_seq_find_class(cls)\n")
	g.Printf("C.free(unsafe.Pointer(cls))\n")
	// Before Go 1.11 clazz was a pointer value, an uintptr after.
	g.Printf("if uintptr(clazz) == 0 {\n")
	g.Printf("	return\n")
	g.Printf("}\n")
	g.Printf("class_%s = clazz\n", cls.JNIName)
	for _, fs := range cls.Funcs {
		var supported bool
		for _, f := range fs.Funcs {
			if f.Public && g.isFuncSupported(f) {
				supported = true
				break
			}
		}
		if !supported {
			continue
		}
		g.Printf("%s.%s = func", cls.PkgName, fs.GoName)
		g.genFuncDecl(false, fs)
		g.genFuncBody(cls, fs, "cproxy_s", true)
	}
	g.Printf("%s.Cast = func(v interface{}) Java.%s {\n", cls.PkgName, goClsName(cls.Name))
	g.Indent()
	g.Printf("t := reflect.TypeOf((*proxy_class_%s)(nil))\n", cls.JNIName)
	g.Printf("cv := reflect.ValueOf(v).Convert(t).Interface().(*proxy_class_%s)\n", cls.JNIName)
	g.Printf("ref := C.jint(_seq.ToRefNum(cv))\n")
	g.Printf("if C.go_seq_isinstanceof(ref, class_%s) != 1 {\n", cls.JNIName)
	g.Printf("	panic(fmt.Errorf(\"%%T is not an instance of %%s\", v, %q))\n", cls.Name)
	g.Printf("}\n")
	g.Printf("return cv\n")
	g.Outdent()
	g.Printf("}\n")
	g.Outdent()
	g.Printf("}\n\n")
	g.Printf("type proxy_class_%s _seq.Ref\n\n", cls.JNIName)
	g.Printf("func (p *proxy_class_%s) Bind_proxy_refnum__() int32 {\n", cls.JNIName)
	g.Indent()
	g.Printf("return (*_seq.Ref)(p).Bind_IncNum()\n")
	g.Outdent()
	g.Printf("}\n\n")
	for _, fs := range cls.AllMethods {
		if !g.isFuncSetSupported(fs) {
			continue
		}
		g.Printf("func (p *proxy_class_%s) %s", cls.JNIName, fs.GoName)
		g.genFuncDecl(false, fs)
		g.genFuncBody(cls, fs, "cproxy", false)
	}
	if cls.Throwable {
		g.Printf("func (p *proxy_class_%s) Error() string {\n", cls.JNIName)
		g.Printf("	return p.ToString()\n")
		g.Printf("}\n")
	}
	if goName, ok := g.goClsMap[cls.Name]; ok {
		g.Printf("func (p *proxy_class_%s) Super() Java.%s {\n", cls.JNIName, goClsName(cls.Name))
		g.Printf("	return &super_%s{p}\n", cls.JNIName)
		g.Printf("}\n\n")
		g.Printf("type super_%s struct {*proxy_class_%[1]s}\n\n", cls.JNIName)
		g.Printf("func (p *proxy_class_%s) Unwrap() interface{} {\n", cls.JNIName)
		g.Indent()
		g.Printf("goRefnum := C.go_seq_unwrap(C.jint(p.Bind_proxy_refnum__()))\n")
		g.Printf("return _seq.FromRefNum(int32(goRefnum)).Get().(*%s)\n", goName)
		g.Outdent()
		g.Printf("}\n\n")
		for _, fs := range cls.AllMethods {
			if !g.isFuncSetSupported(fs) {
				continue
			}
			g.Printf("func (p *super_%s) %s", cls.JNIName, fs.GoName)
			g.genFuncDecl(false, fs)
			g.genFuncBody(cls, fs, "csuper", false)
		}
	}
}

// genFuncBody generated a Go function body for a FuncSet. It resolves overloading dynamically,
// by inspecting the number of arguments (if the FuncSet contains varying parameter counts),
// and their types.
func (g *ClassGen) genFuncBody(cls *java.Class, fs *java.FuncSet, prefix string, static bool) {
	maxp := len(fs.Funcs[0].Params)
	minp := maxp
	// sort the function variants into argument sizes.
	buckets := make(map[int][]*java.Func)
	numF := 0
	for _, f := range fs.Funcs {
		if !g.isFuncSupported(f) {
			continue
		}
		numF++
		n := len(f.Params)
		if n < minp {
			minp = n
		} else if n > maxp {
			maxp = n
		}
		buckets[n] = append(buckets[n], f)
	}
	g.Printf(" {\n")
	g.Indent()
	if len(buckets) != 1 {
		// Switch over the number of arguments.
		g.Printf("switch %d + len(a%d) {\n", minp, minp)
	}
	for i := minp; i <= maxp; i++ {
		funcs := buckets[i]
		if len(funcs) == 0 {
			continue
		}
		if len(buckets) != 1 {
			g.Printf("case %d:\n", i)
			g.Indent()
		}
		for _, f := range funcs {
			if len(funcs) > 1 {
				g.Printf("{\n")
				g.Indent()
			}
			var argNames []string
			var preds []string
			for i, a := range f.Params {
				var ct *java.Type
				var argName string
				if i >= minp {
					argName = fmt.Sprintf("a%d[%d]", minp, i-minp)
					ct = fs.Params[minp]
				} else {
					argName = fmt.Sprintf("a%d", i)
					ct = fs.Params[i]
				}
				if !reflect.DeepEqual(ct, a) {
					g.Printf("_a%d, ok%d := %s.(%s)\n", i, i, argName, g.goType(a, false))
					argName = fmt.Sprintf("_a%d", i)
					preds = append(preds, fmt.Sprintf("ok%d", i))
				}
				argNames = append(argNames, argName)
			}
			if len(preds) > 0 {
				g.Printf("if %s {\n", strings.Join(preds, " && "))
				g.Indent()
			}
			for i, a := range f.Params {
				g.genWrite(fmt.Sprintf("__a%d", i), argNames[i], a, modeTransient)
			}
			g.Printf("res := C.%s_%s_%s(", prefix, cls.JNIName, f.JNIName)
			if !static {
				g.Printf("C.jint(p.Bind_proxy_refnum__())")
			}
			for i := range f.Params {
				if !static || i > 0 {
					g.Printf(", ")
				}
				g.Printf("__a%d", i)
			}
			g.Printf(")\n")
			g.genFuncRet(fs, f, numF > 1)
			if len(preds) > 0 {
				g.Outdent()
				g.Printf("}\n")
			}
			if len(funcs) > 1 {
				g.Outdent()
				g.Printf("}\n")
			}
		}
		if len(buckets) != 1 {
			g.Outdent()
		}
	}
	if len(buckets) != 1 {
		g.Printf("}\n")
	}
	if numF > 1 {
		g.Printf("panic(\"no overloaded method found for %s.%s that matched the arguments\")\n", cls.Name, fs.Name)
	}
	g.Outdent()
	g.Printf("}\n\n")
}

func (g *ClassGen) genFuncRet(fs *java.FuncSet, f *java.Func, mustReturn bool) {
	if f.Ret != nil {
		g.genRead("_res", "res.res", f.Ret, modeRetained)
		g.genRefRead("_exc", "res.exc", "error", "proxy_error", true)
	} else {
		g.genRefRead("_exc", "res", "error", "proxy_error", true)
	}
	if !fs.Throws {
		g.Printf("if (_exc != nil) { panic(_exc) }\n")
		if fs.HasRet {
			if f.Ret != nil {
				g.Printf("return _res\n")
			} else {
				// The variant doesn't return a value, but the common
				// signature does. Use nil as a placeholder return value.
				g.Printf("return nil\n")
			}
		} else if mustReturn {
			// If there are overloaded variants, return here to avoid the fallback
			// panic generated in genFuncBody.
			g.Printf("return\n")
		}
	} else {
		if fs.HasRet {
			if f.Ret != nil {
				g.Printf("return _res, _exc\n")
			} else {
				// As above, use a nil placeholder return value.
				g.Printf("return nil, _exc\n")
			}
		} else {
			g.Printf("return _exc\n")
		}
	}
}

func (g *ClassGen) genRead(to, from string, t *java.Type, mode varMode) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double:
		g.Printf("%s := %s(%s)\n", to, g.goType(t, false), from)
	case java.Boolean:
		g.Printf("%s := %s != C.JNI_FALSE\n", to, from)
	case java.String:
		g.Printf("%s := decodeString(%s)\n", to, from)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("%s := toSlice(%s, %v)\n", to, from, mode == modeRetained)
	case java.Object:
		_, hasProxy := g.imported[t.Class]
		g.genRefRead(to, from, g.goType(t, false), "proxy_class_"+flattenName(t.Class), hasProxy)
	default:
		panic("invalid kind")
	}
}

func (g *ClassGen) genRefRead(to, from string, intfName, proxyName string, hasProxy bool) {
	g.Printf("var %s %s\n", to, intfName)
	g.Printf("%s_ref := _seq.FromRefNum(int32(%s))\n", to, from)
	g.Printf("if %s_ref != nil {\n", to)
	g.Printf("	if %s < 0 { // go object\n", from)
	g.Printf("		%s = %s_ref.Get().(%s)\n", to, to, intfName)
	g.Printf("	} else { // foreign object\n")
	if hasProxy {
		g.Printf("		%s = (*%s)(%s_ref)\n", to, proxyName, to)
	} else {
		g.Printf("		%s = %s_ref\n", to, to)
	}
	g.Printf("	}\n")
	g.Printf("}\n")
}

func (g *ClassGen) genWrite(dst, v string, t *java.Type, mode varMode) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double:
		g.Printf("%s := C.%s(%s)\n", dst, t.CType(), v)
	case java.Boolean:
		g.Printf("%s := C.jboolean(C.JNI_FALSE)\n", dst)
		g.Printf("if %s {\n", v)
		g.Printf("	%s = C.jboolean(C.JNI_TRUE)\n", dst)
		g.Printf("}\n")
	case java.String:
		g.Printf("%s := encodeString(%s)\n", dst, v)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("%s := fromSlice(%s, %v)\n", dst, v, mode == modeRetained)
	case java.Object:
		g.Printf("var %s C.jint = _seq.NullRefNum\n", dst)
		g.Printf("if %s != nil {\n", v)
		g.Printf("	%s = C.jint(_seq.ToRefNum(%s))\n", dst, v)
		g.Printf("}\n")
	default:
		panic("invalid kind")
	}
}

// genCRetClear clears the result value from a JNI call if an exception was
// raised.
func (g *ClassGen) genCRetClear(v string, t *java.Type, exc string) {
	g.Printf("if (%s != NULL) {\n", exc)
	g.Indent()
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double, java.Boolean:
		g.Printf("%s = 0;\n", v)
	default:
		// Assume a nullable type. It will break if we missed a type.
		g.Printf("%s = NULL;\n", v)
	}
	g.Outdent()
	g.Printf("}\n")
}

func (g *ClassGen) genJavaToC(v string, t *java.Type) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double, java.Boolean:
		g.Printf("%s _%s = %s;\n", t.JNIType(), v, v)
	case java.String:
		g.Printf("nstring _%s = go_seq_from_java_string(env, %s);\n", v, v)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("nbyteslice _%s = go_seq_from_java_bytearray(env, %s, 1);\n", v, v)
	case java.Object:
		g.Printf("jint _%s = go_seq_to_refnum(env, %s);\n", v, v)
	default:
		panic("invalid kind")
	}
}

func (g *ClassGen) genCToJava(v string, t *java.Type) {
	switch t.Kind {
	case java.Int, java.Short, java.Char, java.Byte, java.Long, java.Float, java.Double, java.Boolean:
		g.Printf("%s _%s = %s;\n", t.JNIType(), v, v)
	case java.String:
		g.Printf("jstring _%s = go_seq_to_java_string(env, %s);\n", v, v)
	case java.Array:
		if t.Elem.Kind != java.Byte {
			panic("unsupported array type")
		}
		g.Printf("jbyteArray _%s = go_seq_to_java_bytearray(env, %s, 0);\n", v, v)
	case java.Object:
		g.Printf("jobject _%s = go_seq_from_refnum(env, %s, NULL, NULL);\n", v, v)
	default:
		panic("invalid kind")
	}
}

func goClsName(n string) string {
	return initialUpper(strings.Replace(n, ".", "_", -1))
}

func (g *ClassGen) genInterface(cls *java.Class) {
	g.Printf("type %s interface {\n", goClsName(cls.Name))
	g.Indent()
	// Methods
	for _, fs := range cls.AllMethods {
		if !g.isFuncSetSupported(fs) {
			continue
		}
		g.Printf(fs.GoName)
		g.genFuncDecl(true, fs)
		g.Printf("\n")
	}
	if goName, ok := g.goClsMap[cls.Name]; ok {
		g.Printf("Super() %s\n", goClsName(cls.Name))
		g.Printf("// Unwrap returns the Go object this Java instance\n")
		g.Printf("// is wrapping.\n")
		g.Printf("// The return value is a %s, but the delclared type is\n", goName)
		g.Printf("// interface{} to avoid import cycles.\n")
		g.Printf("Unwrap() interface{}\n")
	}
	if cls.Throwable {
		g.Printf("Error() string\n")
	}
	g.Outdent()
	g.Printf("}\n\n")
}

// Flatten java class names. "java.package.Class$Inner" is converted to
// "java_package_Class_Inner"
func flattenName(n string) string {
	return strings.Replace(strings.Replace(n, ".", "_", -1), "$", "_", -1)
}

var (
	classesPkgHeader = gobindPreamble + `
package Java

// Used to silence this package not used errors
const Dummy = 0

`
	classesCHeader = gobindPreamble + `
#include <jni.h>
#include "seq.h"
#include "classes.h"

`
	classesHHeader = gobindPreamble + `
#include <jni.h>
#include "seq.h"

extern void init_proxies();

`

	javaImplHeader = gobindPreamble

	classesGoHeader = gobindPreamble + `
package main

/*
#include <stdlib.h> // for free()
#include <jni.h>
#include "seq.h"
#include "classes.h"
*/
import "C"

import (
	"Java"
	_seq "github.com/baguettex/gomobile/bind/seq"
)

`
)
