/*
Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements.  See the NOTICE file
distributed with this work for additional information
regarding copyright ownership.  The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License.  You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied.  See the License for the
specific language governing permissions and limitations
under the License.
*/

// Code generator to generate a thin Go wrapper API around the C proton API.
//

package main

import (
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"text/template"
)

func mixedCase(s string) string {
	result := ""
	for _, w := range strings.Split(s, "_") {
		if w != "" {
			result = result + strings.ToUpper(w[0:1]) + strings.ToLower(w[1:])
		}
	}
	return result
}

func mixedCaseTrim(s, prefix string) string {
	return mixedCase(strings.TrimPrefix(s, prefix))
}

var templateFuncs = template.FuncMap{"mixedCase": mixedCase, "mixedCaseTrim": mixedCaseTrim}

func doTemplate(out io.Writer, data interface{}, tmpl string) {
	panicIf(template.Must(template.New("").Funcs(templateFuncs).Parse(tmpl)).Execute(out, data))
}

type enumType struct {
	Name   string
	Values []string
}

// Find enums in a header file return map of enum name to values.
func findEnums(header string) (enums []enumType) {
	for _, enum := range enumDefRe.FindAllStringSubmatch(header, -1) {
		enums = append(enums, enumType{enum[2], enumValRe.FindAllString(enum[1], -1)})
	}
	return enums
}

func genEnum(out io.Writer, name string, values []string) {
	doTemplate(out, []interface{}{name, values}, `{{$enumName := index . 0}}{{$values := index . 1}}
type {{mixedCase $enumName}} C.pn_{{$enumName}}_t
const ({{range $values}}
	{{mixedCase .}} {{mixedCase $enumName}} = C.{{.}} {{end}}
)

func (e {{mixedCase $enumName}}) String() string {
	switch e {
{{range $values}}
	case C.{{.}}: return "{{mixedCaseTrim . "PN_"}}" {{end}}
	}
	return "unknown"
}
`)
}

var (
	reSpace = regexp.MustCompile("\\s+")
)

func panicIf(err error) {
	if err != nil {
		panic(err)
	}
}

func readHeader(name string) string {
	file, err := os.Open(path.Join(*includeProton, name+".h"))
	panicIf(err)
	defer file.Close()
	s, err := ioutil.ReadAll(file)
	panicIf(err)
	return string(s)
}

var copyright string = `/*
Licensed to the Apache Software Foundation (ASF) under one
or more contributor license agreements.  See the NOTICE file
distributed with this work for additional information
regarding copyright ownership.  The ASF licenses this file
to you under the Apache License, Version 2.0 (the
"License"); you may not use this file except in compliance
with the License.  You may obtain a copy of the License at

  http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing,
software distributed under the License is distributed on an
"AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
KIND, either express or implied.  See the License for the
specific language governing permissions and limitations
under the License.
*/

//
// NOTE: This file was generated by genwrap.go, do not edit it by hand.
//
`

type eventType struct {
	// C, function and interface names for the event
	Name, Cname, Fname, Iname string
}

func newEventType(cName string) eventType {
	var etype eventType
	etype.Cname = cName
	etype.Name = mixedCaseTrim(cName, "PN_")
	etype.Fname = "On" + etype.Name
	etype.Iname = etype.Fname + "Interface"
	return etype
}

var (
	enumDefRe   = regexp.MustCompile("typedef enum {([^}]*)} pn_([a-z_]+)_t;")
	enumValRe   = regexp.MustCompile("PN_[A-Z_]+")
	skipEventRe = regexp.MustCompile("EVENT_NONE|REACTOR|SELECTABLE|TIMER")
	skipFnRe    = regexp.MustCompile("attach|context|class|collect|^recv$|^send$|transport")
)

// Generate event wrappers.
func event(out io.Writer) {
	event_h := readHeader("event")

	// Event is implented by hand in wrappers.go

	// Get all the pn_event_type_t enum values
	var etypes []eventType
	enums := findEnums(event_h)
	for _, e := range enums[0].Values {
		if skipEventRe.FindStringSubmatch(e) == nil {
			etypes = append(etypes, newEventType(e))
		}
	}

	doTemplate(out, etypes, `
type EventType int
const ({{range .}}
	 E{{.Name}} EventType = C.{{.Cname}}{{end}}
)
`)

	doTemplate(out, etypes, `
func (e EventType) String() string {
	switch e {
{{range .}}
	case C.{{.Cname}}: return "{{.Name}}"{{end}}
	}
	return "Unknown"
}
`)
}

type genType struct {
	Ctype, Gotype string
	ToGo          func(value string) string
	ToC           func(value string) string
	Assign        func(value string) string
}

func (g genType) printBody(out io.Writer, value string) {
	if g.Gotype != "" {
		fmt.Fprintf(out, "return %s", g.ToGo(value))
	} else {
		fmt.Fprintf(out, "%s", value)
	}
}

func (g genType) goLiteral(value string) string {
	return fmt.Sprintf("%s{%s}", g.Gotype, value)
}

func (g genType) goConvert(value string) string {
	switch g.Gotype {
	case "string":
		return fmt.Sprintf("C.GoString(%s)", value)
	case "Event":
		return fmt.Sprintf("makeEvent(%s)", value)
	default:
		return fmt.Sprintf("%s(%s)", g.Gotype, value)
	}
}

var notStruct = map[string]bool{
	"EventType":        true,
	"SndSettleMode":    true,
	"RcvSettleMode":    true,
	"TerminusType":     true,
	"State":            true,
	"Durability":       true,
	"ExpiryPolicy":     true,
	"DistributionMode": true,
}

func mapType(ctype string) (g genType) {
	g.Ctype = "C." + strings.Trim(ctype, " \n")

	switch g.Ctype {
	case "C.void":
		g.Gotype = ""
	case "C.size_t":
		g.Gotype = "uint"
	case "C.int":
		g.Gotype = "int"
	case "C.void *":
		g.Gotype = "unsafe.Pointer"
		g.Ctype = "unsafe.Pointer"
	case "C.bool":
		g.Gotype = "bool"
	case "C.ssize_t":
		g.Gotype = "int"
	case "C.uint64_t":
		g.Gotype = "uint64"
	case "C.uint32_t":
		g.Gotype = "uint16"
	case "C.uint16_t":
		g.Gotype = "uint32"
	case "C.const char *":
		fallthrough
	case "C.char *":
		g.Gotype = "string"
		g.Ctype = "C.CString"
		g.ToC = func(v string) string { return fmt.Sprintf("%sC", v) }
		g.Assign = func(v string) string {
			return fmt.Sprintf("%sC := C.CString(%s)\n defer C.free(unsafe.Pointer(%sC))\n", v, v, v)
		}
	case "C.pn_seconds_t":
		g.Gotype = "time.Duration"
		g.ToGo = func(v string) string { return fmt.Sprintf("(time.Duration(%s) * time.Second)", v) }
	case "C.pn_error_t *":
		g.Gotype = "error"
		g.ToGo = func(v string) string { return fmt.Sprintf("internal.PnError(unsafe.Pointer(%s))", v) }
	default:
		pnId := regexp.MustCompile(" *pn_([a-z_]+)_t *\\*? *")
		match := pnId.FindStringSubmatch(g.Ctype)
		if match == nil {
			panic(fmt.Errorf("unknown C type %#v", g.Ctype))
		}
		g.Gotype = mixedCase(match[1])
		if !notStruct[g.Gotype] {
			g.ToGo = g.goLiteral
			g.ToC = func(v string) string { return v + ".pn" }
		}
	}
	if g.ToGo == nil {
		g.ToGo = g.goConvert // Use conversion by default.
	}
	if g.ToC == nil {
		g.ToC = func(v string) string { return fmt.Sprintf("%s(%s)", g.Ctype, v) }
	}
	return
}

type genArg struct {
	Name string
	genType
}

var typeNameRe = regexp.MustCompile("^(.*( |\\*))([^ *]+)$")

func splitArgs(argstr string) []genArg {
	argstr = strings.Trim(argstr, " \n")
	if argstr == "" {
		return []genArg{}
	}
	args := make([]genArg, 0)
	for _, item := range strings.Split(argstr, ",") {
		item = strings.Trim(item, " \n")
		typeName := typeNameRe.FindStringSubmatch(item)
		if typeName == nil {
			panic(fmt.Errorf("Can't split argument type/name %#v", item))
		}
		cType := strings.Trim(typeName[1], " \n")
		name := strings.Trim(typeName[3], " \n")
		if name == "type" {
			name = "type_"
		}
		args = append(args, genArg{name, mapType(cType)})
	}
	return args
}

func goArgs(args []genArg) string {
	l := ""
	for i, arg := range args {
		if i != 0 {
			l += ", "
		}
		l += arg.Name + " " + arg.Gotype
	}
	return l
}

func cArgs(args []genArg) string {
	l := ""
	for _, arg := range args {
		l += fmt.Sprintf(", %s", arg.ToC(arg.Name))
	}
	return l
}

func cAssigns(args []genArg) string {
	l := "\n"
	for _, arg := range args {
		if arg.Assign != nil {
			l += fmt.Sprintf("%s\n", arg.Assign(arg.Name))
		}
	}
	return l
}

// Return the go name of the function or "" to skip the function.
func goFnName(api, fname string) string {
	// Skip class, context and attachment functions.
	if skipFnRe.FindStringSubmatch(fname) != nil {
		return ""
	}
	switch api + "." + fname {
	case "link.get_drain":
		return "IsDrain"
	default:
		return mixedCaseTrim(fname, "get_")
	}
}

func apiWrapFns(api, header string, out io.Writer) {
	fmt.Fprintf(out, "type %s struct{pn *C.pn_%s_t}\n", mixedCase(api), api)
	fmt.Fprintf(out, "func (%c %s) IsNil() bool { return %c.pn == nil }\n", api[0], mixedCase(api), api[0])
	fn := regexp.MustCompile(fmt.Sprintf(`PN_EXTERN ([a-z0-9_ ]+ *\*?) *pn_%s_([a-z_]+)\(pn_%s_t *\*[a-z_]+ *,? *([^)]*)\)`, api, api))
	for _, m := range fn.FindAllStringSubmatch(header, -1) {
		rtype, fname, argstr := mapType(m[1]), m[2], m[3]
		gname := goFnName(api, fname)
		if gname == "" { // Skip
			continue
		}
		args := splitArgs(argstr)
		fmt.Fprintf(out, "func (%c %s) %s", api[0], mixedCase(api), gname)
		fmt.Fprintf(out, "(%s) %s { ", goArgs(args), rtype.Gotype)
		fmt.Fprint(out, cAssigns(args))
		rtype.printBody(out, fmt.Sprintf("C.pn_%s_%s(%c.pn%s)", api, fname, api[0], cArgs(args)))
		fmt.Fprintf(out, "}\n")
	}
}

var includeProton = flag.String("include", "", "path to proton include files, including /proton")

func main() {
	flag.Parse()
	outpath := "wrappers_gen.go"
	out, err := os.Create(outpath)
	panicIf(err)
	defer out.Close()

	apis := []string{"session", "link", "delivery", "disposition", "condition", "terminus", "connection"}
	fmt.Fprintln(out, copyright)
	fmt.Fprint(out, `
package event

import (
	"time"
  "unsafe"
  "qpid.apache.org/proton/go/internal"
)

// #include <proton/types.h>
// #include <proton/event.h>
// #include <stdlib.h>
`)
	for _, api := range apis {
		fmt.Fprintf(out, "// #include <proton/%s.h>\n", api)
	}
	fmt.Fprintln(out, `import "C"`)

	event(out)

	for _, api := range apis {
		fmt.Fprintf(out, "// Wrappers for declarations in %s.h\n\n", api)
		header := readHeader(api)
		enums := findEnums(header)
		for _, e := range enums {
			genEnum(out, e.Name, e.Values)
		}
		apiWrapFns(api, header, out)
	}
	out.Close()

	// Run gofmt.
	cmd := exec.Command("gofmt", "-w", outpath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gofmt: %s", err)
		os.Exit(1)
	}
}
