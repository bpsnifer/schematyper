package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/gedex/inflector"
)

//go:generate schematyper -root-type=metaSchema -prefix=meta metaschema.json

var (
	outToStdout     = flag.Bool("c", false, `output to console; overrides "-o"`)
	outputFile      = flag.String("o", "", "output file name; default is <schema>_schematype.go")
	packageName     = flag.String("package", "main", `package name for generated file; default is "main"`)
	rootTypeName    = flag.String("root-type", "", `name of root type; default is generated from the filename`)
	typeNamesPrefix = flag.String("prefix", "", `prefix for non-root types`)
)

type structField struct {
	Name         string
	Type         string
	Nullable     bool
	PropertyName string
	Required     bool
}

type structFields []structField

func (s structFields) Len() int {
	return len(s)
}

func (s structFields) Less(i, j int) bool {
	return s[i].Name < s[j].Name
}

func (s structFields) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

type goType struct {
	Name     string
	Type     string
	Nullable bool
	Fields   structFields
	Comment  string
}

func (gt goType) print(buf *bytes.Buffer) {
	if gt.Comment != "" {
		buf.WriteString(fmt.Sprintf("// %s\n", gt.Comment))
	}
	buf.WriteString(fmt.Sprintf("type %s %s", gt.Name, gt.Type))
	if gt.Type != "struct" {
		buf.WriteString("\n")
		return
	}
	buf.WriteString(" {\n")
	sort.Stable(gt.Fields)
	for _, sf := range gt.Fields {
		var typeString string
		if sf.Nullable && sf.Type != "interface{}" {
			typeString = "*"
		}
		typeString += sf.Type

		tagString := "`json:\"" + sf.PropertyName
		if !sf.Required {
			tagString += ",omitempty"
		}
		tagString += "\"`"
		buf.WriteString(fmt.Sprintf("%s %s %s\n", sf.Name, typeString, tagString))
	}
	buf.WriteString("}\n")
}

type goTypes []goType

func (t goTypes) Len() int {
	return len(t)
}

func (t goTypes) Less(i, j int) bool {
	return t[i].Name < t[j].Name
}

func (t goTypes) Swap(i, j int) {
	t[i], t[j] = t[j], t[i]
}

var needTimeImport bool

func getTypeString(jsonType, format string) string {
	if format == "date-time" {
		needTimeImport = true
		return "time.Time"
	}

	switch jsonType {
	case "string":
		return "string"
	case "integer":
		return "int"
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "null":
		return "nil"
	case "array":
		fallthrough
	case "object":
		return jsonType
	default:
		return "interface{}"
	}
}

// copied from golint (https://github.com/golang/lint/blob/4946cea8b6efd778dc31dc2dbeb919535e1b7529/lint.go#L701)
var commonInitialisms = map[string]bool{
	"API":   true,
	"ASCII": true,
	"CPU":   true,
	"CSS":   true,
	"DNS":   true,
	"EOF":   true,
	"GUID":  true,
	"HTML":  true,
	"HTTP":  true,
	"HTTPS": true,
	"ID":    true,
	"IP":    true,
	"JSON":  true,
	"LHS":   true,
	"QPS":   true,
	"RAM":   true,
	"RHS":   true,
	"RPC":   true,
	"SLA":   true,
	"SMTP":  true,
	"SQL":   true,
	"SSH":   true,
	"TCP":   true,
	"TLS":   true,
	"TTL":   true,
	"UDP":   true,
	"UI":    true,
	"UID":   true,
	"UUID":  true,
	"URI":   true,
	"URL":   true,
	"UTF8":  true,
	"VM":    true,
	"XML":   true,
	"XSRF":  true,
	"XSS":   true,
}

func dashedToWords(s string) string {
	return regexp.MustCompile("-|_").ReplaceAllString(s, " ")
}

func camelCaseToWords(s string) string {
	return regexp.MustCompile(`([\p{Ll}\p{N}])(\p{Lu})`).ReplaceAllString(s, "$1 $2")
}

func getExportedIdentifierPart(part string) string {
	upperedPart := strings.ToUpper(part)
	if commonInitialisms[upperedPart] {
		return upperedPart
	}
	return strings.Title(strings.ToLower(part))
}

func generateIdentifier(origName string, exported bool) string {
	spacedName := camelCaseToWords(dashedToWords(origName))
	titledName := strings.Title(spacedName)
	nameParts := strings.Split(titledName, " ")
	for i, part := range nameParts {
		nameParts[i] = getExportedIdentifierPart(part)
	}
	if !exported {
		nameParts[0] = strings.ToLower(nameParts[0])
	}
	rawName := strings.Join(nameParts, "")

	// make sure we build a valid identifier
	buf := &bytes.Buffer{}
	for pos, char := range rawName {
		if unicode.IsLetter(char) || char == '_' || (unicode.IsDigit(char) && pos > 0) {
			buf.WriteRune(char)
		}
	}

	return buf.String()
}

func generateTypeName(origName string) string {
	if *packageName != "main" || *typeNamesPrefix != "" {
		return *typeNamesPrefix + generateIdentifier(origName, true)
	}

	return generateIdentifier(origName, false)
}

func generateFieldName(origName string) string {
	return generateIdentifier(origName, true)
}

func getTypeSchema(typeInterface interface{}) *metaSchema {
	typeSchemaJSON, _ := json.Marshal(typeInterface)
	var typeSchema metaSchema
	json.Unmarshal(typeSchemaJSON, &typeSchema)
	return &typeSchema
}

func getTypeSchemas(typeInterface interface{}) map[string]*metaSchema {
	typeSchemasJSON, _ := json.Marshal(typeInterface)
	var typeSchemas map[string]*metaSchema
	json.Unmarshal(typeSchemasJSON, &typeSchemas)
	return typeSchemas
}

func singularize(plural string) string {
	singular := inflector.Singularize(plural)
	if singular == plural {
		singular += "Item"
	}
	return singular
}

func parseAdditionalProperties(ap interface{}) (hasAddl bool, addlSchema *metaSchema) {
	switch ap := ap.(type) {
	case bool:
		return ap, nil
	case map[string]interface{}:
		return true, getTypeSchema(ap)
	default:
		return
	}
}

type deferredType struct {
	schema *metaSchema
	name   string
	desc   string
}

var types = make(map[string]goType)
var deferredTypes = make(map[string]deferredType)

func processType(s *metaSchema, pName, pDesc, path string) (typeName string) {
	var gt goType

	// avoid 'recursive type' problem, at least for the root type
	if path == "#" {
		gt.Nullable = true
	}

	if s.Ref != "" {
		if refType, ok := types[s.Ref]; ok {
			return refType.Name
		}
		deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
		return ""
	}

	defer func() { types[path] = gt }()

	var origTypeName string
	if path == "#" {
		origTypeName = *rootTypeName
		gt.Name = *rootTypeName
	} else {
		origTypeName = s.Title
		if origTypeName == "" {
			origTypeName = pName
		}

		if gt.Name = generateTypeName(origTypeName); gt.Name == "" {
			log.Fatalln("Can't generate type without name.")
		}
	}

	typeName = gt.Name

	gt.Comment = s.Description
	if gt.Comment == "" {
		gt.Comment = pDesc
	}

	required := make(map[string]bool)
	for _, req := range s.Required {
		required[string(req)] = true
	}

	var jsonType string
	switch schemaType := s.Type.(type) {
	case []interface{}:
		if len(schemaType) == 2 && (schemaType[0] == "null" || schemaType[1] == "null") {
			gt.Nullable = true

			jsonType = schemaType[0].(string)
			if jsonType == "null" {
				jsonType = schemaType[1].(string)
			}
		}
	case string:
		jsonType = schemaType
	}

	props := getTypeSchemas(s.Properties)
	hasProps := len(props) > 0
	hasAddlProps, addlPropsSchema := parseAdditionalProperties(s.AdditionalProperties)

	typeString := getTypeString(jsonType, s.Format)
	switch typeString {
	case "object":
		if gt.Name == "Properties" {
			panic(fmt.Errorf("props: %+v\naddlPropsSchema: %+v\n", props, addlPropsSchema))
		}
		if hasProps && !hasAddlProps {
			gt.Type = "struct"
		} else if !hasProps && hasAddlProps && addlPropsSchema != nil {
			singularName := singularize(origTypeName)
			gotType := processType(addlPropsSchema, singularName, s.Description, path+"/additionalProperties")
			if gotType == "" {
				deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
				return ""
			}
			gt.Type = "map[string]" + gotType
		} else {
			gt.Type = "map[string]interface{}"
		}
	case "array":
		switch arrayItemType := s.Items.(type) {
		case []interface{}:
			if len(arrayItemType) == 1 {
				singularName := singularize(origTypeName)
				typeSchema := getTypeSchema(arrayItemType[0])
				gotType := processType(typeSchema, singularName, s.Description, path+"/items/0")
				if gotType == "" {
					deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
					return ""
				}
				gt.Type = "[]" + gotType
			} else {
				gt.Type = "[]interface{}"
			}
		case interface{}:
			singularName := singularize(origTypeName)
			typeSchema := getTypeSchema(arrayItemType)
			gotType := processType(typeSchema, singularName, s.Description, path+"/items")
			if gotType == "" {
				deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
				return ""
			}
			gt.Type = "[]" + gotType
		default:
			gt.Type = "[]interface{}"
		}
	default:
		gt.Type = typeString
	}

	for propName, propSchema := range props {
		sf := structField{
			PropertyName: propName,
			Required:     required[propName],
		}

		var fieldName string
		if propSchema.Title != "" {
			fieldName = propSchema.Title
		} else {
			fieldName = propName
		}
		if sf.Name = generateFieldName(fieldName); sf.Name == "" {
			log.Fatalln("Can't generate field without name.")
		}

		if propSchema.Ref != "" {
			if refType, ok := types[propSchema.Ref]; ok {
				sf.Type, sf.Nullable = refType.Name, refType.Nullable
				gt.Fields = append(gt.Fields, sf)
				continue
			}
			deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
			return ""
		}

		switch propType := propSchema.Type.(type) {
		case []interface{}:
			if len(propType) == 2 && (propType[0] == "null" || propType[1] == "null") {
				sf.Nullable = true

				jsonType := propType[0]
				if jsonType == "null" {
					jsonType = propType[1]
				}

				sf.Type = getTypeString(jsonType.(string), propSchema.Format)
			}
		case string:
			sf.Type = getTypeString(propType, propSchema.Format)
		case nil:
			sf.Type = "interface{}"
		}

		refPath := path + "/properties/" + propName

		props := getTypeSchemas(propSchema.Properties)
		hasProps := len(props) > 0
		hasAddlProps, addlPropsSchema := parseAdditionalProperties(propSchema.AdditionalProperties)

		if sf.Type == "object" {
			if hasProps && !hasAddlProps {
				sf.Type = processType(propSchema, sf.Name, propSchema.Description, refPath)
			} else if !hasProps && hasAddlProps && addlPropsSchema != nil {
				singularName := singularize(propName)
				gotType := processType(addlPropsSchema, singularName, propSchema.Description, refPath+"/additionalProperties")
				if gotType == "" {
					deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
					return ""
				}
				sf.Type = "map[string]" + gotType
			} else {
				sf.Type = "map[string]interface{}"
			}
		} else if sf.Type == "array" {
			switch arrayItemType := propSchema.Items.(type) {
			case []interface{}:
				if len(arrayItemType) == 1 {
					singularName := singularize(propName)
					typeSchema := getTypeSchema(arrayItemType[0])
					gotType := processType(typeSchema, singularName, propSchema.Description, refPath+"/items/0")
					if gotType == "" {
						deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
						return ""
					}
					sf.Type = "[]" + gotType
				} else {
					sf.Type = "[]interface{}"
				}
			case interface{}:
				singularName := singularize(propName)
				typeSchema := getTypeSchema(arrayItemType)
				gotType := processType(typeSchema, singularName, propSchema.Description, refPath+"/items")
				if gotType == "" {
					deferredTypes[path] = deferredType{schema: s, name: pName, desc: pDesc}
					return ""
				}
				sf.Type = "[]" + gotType
			default:
				sf.Type = "[]interface{}"
			}
		}

		gt.Fields = append(gt.Fields, sf)
	}

	return
}

func processDeferred() {
	for len(deferredTypes) > 0 {
		for path, deferred := range deferredTypes {
			name := processType(deferred.schema, deferred.name, deferred.desc, path)
			if name != "" {
				delete(deferredTypes, path)
			}
		}
	}
}

func parseDefs(s *metaSchema) {
	defs := getTypeSchemas(s.Definitions)
	for defName, defSchema := range defs {
		name := processType(defSchema, defName, defSchema.Description, "#/definitions/"+defName)
		if name == "" {
			deferredTypes["#/definitions/"+defName] = deferredType{schema: defSchema, name: defName, desc: defSchema.Description}
		}
	}
}

func main() {
	flag.Parse()

	if flag.NArg() == 0 {
		log.Fatalln("No file to parse.")
	}

	file, err := ioutil.ReadFile(flag.Arg(0))
	if err != nil {
		log.Fatalln("Error reading file:", err)
	}

	var s metaSchema
	if err := json.Unmarshal(file, &s); err != nil {
		log.Fatalln("Error parsing JSON:", err)
	}

	parseDefs(&s)

	schemaName := strings.Split(filepath.Base(flag.Arg(0)), ".")[0]
	if *rootTypeName == "" {
		*rootTypeName = schemaName
	}
	processType(&s, *rootTypeName, s.Description, "#")
	processDeferred()

	var resultSrc bytes.Buffer
	resultSrc.WriteString(fmt.Sprintln("package", *packageName))
	resultSrc.WriteString(fmt.Sprintf("\n// generated by \"%s\" -- DO NOT EDIT\n", strings.Join(os.Args, " ")))
	resultSrc.WriteString("\n")
	if needTimeImport {
		resultSrc.WriteString("import \"time\"\n")
	}
	typesSlice := make(goTypes, 0, len(types))
	for _, gt := range types {
		typesSlice = append(typesSlice, gt)
	}
	sort.Stable(typesSlice)
	for _, gt := range typesSlice {
		gt.print(&resultSrc)
		resultSrc.WriteString("\n")
	}
	formattedSrc, err := format.Source(resultSrc.Bytes())
	if err != nil {
		fmt.Println(resultSrc.String())
		log.Fatalln("Error running gofmt:", err)
	}

	if *outToStdout {
		fmt.Print(string(formattedSrc))
	} else {
		outputFileName := *outputFile
		if outputFileName == "" {
			compactSchemaName := strings.ToLower(*rootTypeName)
			outputFileName = fmt.Sprintf("%s_schematype.go", compactSchemaName)
		}
		err = ioutil.WriteFile(outputFileName, formattedSrc, 0644)
		if err != nil {
			log.Fatalf("Error writing to %s: %s\n", outputFileName, err)
		}
	}
}
