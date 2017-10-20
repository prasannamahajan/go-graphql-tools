package tools

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/prasannamahajan/graphql"
	"github.com/graphql-go/graphql/language/ast"
	"golang.org/x/net/context"
)

const (
	ArgTypeParams  = 1
	ArgTypeSource  = 2
	ArgTypeArgs    = 3
	ArgTypeContext = 4
)

type ResolveParams struct {
	FieldInfo FieldInfo
	Source    interface{}
	Args      interface{}
	Context   interface{}
	Params    graphql.ResolveParams
}

type Router struct {
	queries  map[string]RouteParams
	resolves map[string]RouteParams
	uses     []UseFn
}
type RouteParams struct {
	Args    []int
	Context reflect.Type
	Handle  interface{}
}

func NewRouter() *Router {
	router := Router{
		queries:  map[string]RouteParams{},
		resolves: map[string]RouteParams{},
		uses:     []UseFn{},
	}
	return &router
}

func (r *Router) Routes() map[string]interface{} {
	routes := map[string]interface{}{}
	for k, _ := range r.queries {
		routes[k] = r.Resolve
	}
	return routes
}
func (r *Router) IsResolve(sourceType reflect.Type, field reflect.StructField) bool {
	path := sourceType.Name() + "." + field.Name
	if _, ok := r.queries[path]; ok {
		return true
	}
	resolveTag := field.Tag.Get("resolve")
	if resolveTag != "" {
		resolveTagParams := strings.Split(resolveTag, ",")
		if resolve, ok := r.resolves[resolveTagParams[0]]; ok {
			r.queries[path] = resolve
			return true
		}
	}

	method, ok := sourceType.MethodByName("Resolve" + field.Name)
	if ok {
		r.Query(path, method.Func.Interface())
		return true
	}

	return false
}

/**
**/
func (r *Router) SourceForResolve(fieldInfo FieldInfo, p graphql.ResolveParams) (interface{}, error) {
	if reflect.TypeOf(p.Source).Kind() == reflect.Map {
		sourceType := reflect.TypeOf(fieldInfo.Source)
		if sourceType.Kind() == reflect.Ptr {
			return reflect.New(sourceType).Interface(), nil
		} else {
			return reflect.New(sourceType).Elem().Interface(), nil
		}
	}
	return p.Source, nil
	sourceType := reflect.TypeOf(fieldInfo.Source)
	sourceValueType := reflect.TypeOf(p.Source)
	//Change ptr to elem
	if sourceType.Kind() == reflect.Ptr {
		sourceType = sourceType.Elem()
	}
	var source interface{}
	//Check type of source
	if sourceValueType.Kind() == reflect.Ptr {
		source = reflect.ValueOf(p.Source).Elem().Interface()

	} else {
		if sourceValueType.Kind() != reflect.Struct {
			if sourceValueType.Kind() == reflect.Map {

				source = reflect.New(sourceType).Elem().Interface()
			} else {
				return nil, InvalidSourceError{RouterError{Text: "Source for resolve query should be struct or pointer to struct, has " + sourceValueType.Name()}}
			}
		} else {
			source = p.Source
		}
	}
	return source, nil
}

func (r *Router) Resolve(fieldInfo FieldInfo, p graphql.ResolveParams) (interface{}, error) {

	source, err := r.SourceForResolve(fieldInfo, p)
	if err != nil {
		return nil, err
	}

	for _, useFn := range r.uses {
		res, next, err := useFn(ResolveParams{
			FieldInfo: fieldInfo,
			Params:    p,
			Source:    source,
		})

		if err != nil {
			return nil, err
		}
		if !next {
			return res, nil
		}
	}
	if p.Info.Operation.GetOperation() != ast.OperationTypeSubscription {

		res, err := r.ResolveQuery(fieldInfo, p)
		if err != nil {
			return nil, err
		}
		return res, nil
	}

	return nil, errors.New("Unsupported resolve")
}
func (r *Router) ResolveQuery(fieldInfo FieldInfo, p graphql.ResolveParams) (interface{}, error) {
	source, err := r.SourceForResolve(fieldInfo, p)
	if err != nil {
		return nil, err
	}

	sourceType := reflect.TypeOf(source)

	query, ok := r.queries[fieldInfo.Path]

	if !ok {
		return nil, NotFoundRoute{RouterError{Text: "Not found route for path " + fieldInfo.Path + " by source " + sourceType.Name() + "," + sourceType.Kind().String()}}
	}

	var args interface{}
	if fieldInfo.Args != nil {
		//args = getArgsForResolve(p.Args, reflect.TypeOf(fieldInfo.Args)).Interface()
		outArgs := reflect.New(reflect.TypeOf(fieldInfo.Args))

		err := MapToStruct(p.Args, outArgs.Interface())
		if err != nil {
			return nil, errors.New("Invalid args " + err.Error())
		}

		args = outArgs.Elem().Interface()
	} else {
		args = nil
	}

	params := ResolveParams{
		Source:    source,
		Args:      args,
		Context:   p.Context,
		FieldInfo: fieldInfo,
		Params:    p,
	}
	argsCall := []reflect.Value{}

	var contextCall reflect.Value
	if query.Context != nil {
		contextCall = getContextForResolve(p.Context, query.Context)
	} else {
		contextCall = reflect.ValueOf(p.Context)
	}

	for i := 0; i < len(query.Args); i++ {
		switch query.Args[i] {
		case ArgTypeSource:
			argsCall = append(argsCall, reflect.ValueOf(source))
			break
		case ArgTypeArgs:
			if args == nil {

				argsCall = append(argsCall, reflect.ValueOf(map[string]interface{}{}))
			} else {
				argsCall = append(argsCall, reflect.ValueOf(args))
			}

			break
		case ArgTypeContext:

			argsCall = append(argsCall, contextCall)
			break
		case ArgTypeParams:

			argsCall = append(argsCall, reflect.ValueOf(params))
			break
		}
	}
	handleValue := reflect.ValueOf(query.Handle)

	resValue := handleValue.Call(argsCall)
	if resValue[1].Interface() != nil {
		return nil, resValue[1].Interface().(error)
	}

	return resValue[0].Interface(), nil
}

func MapToStruct(input interface{}, output interface{}) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	err = json.Unmarshal(b, output)
	if err != nil {
		return err
	}
	return nil
}

func getArgsForResolve(args map[string]interface{}, typ reflect.Type) reflect.Value {

	var output = reflect.New(typ)
	for key, value := range args {
		n := lU(key)
		if _, ok := typ.FieldByName(n); ok {
			field := output.Elem().FieldByName(n)
			if field.CanInterface() {

				if field.Kind() == reflect.Ptr {
					v := reflect.ValueOf(value)

					if v.Type().Kind() == reflect.Ptr {
						field.Set(v)
					} else {

						field.Set(reflect.New(field.Type().Elem()))
						field.Elem().Set(v)
					}

				} else {
					field.Set(reflect.ValueOf(value))
				}

			}
		}
	}
	return output.Elem()
}
func getContextForResolve(context context.Context, typ reflect.Type) reflect.Value {
	var output = reflect.New(typ)

	for i := 0; i < typ.NumField(); i++ {
		if !output.Elem().Field(i).CanInterface() {
			continue
		}
		value := context.Value(lA(typ.Field(i).Name))
		if value == nil {
			continue
		}
		output.Elem().Field(i).Set(reflect.ValueOf(value))
	}

	return output.Elem()
}

type UseFn func(params ResolveParams) (interface{}, bool, error)

func (r *Router) Use(fn UseFn) {
	r.uses = append(r.uses, fn)
}
func (r *Router) Mutation(name string, handle interface{}) {

}
func (r *Router) UseResolve(name string, handle interface{}) {
	r.resolves[name] = r.getRouteParams(handle)
}
func (r *Router) getRouteParams(handle interface{}) RouteParams {
	handleType := reflect.TypeOf(handle)
	if handleType.Kind() != reflect.Func {
		panic("Invalid query handle, expected func, has " + handleType.Kind().String())
	}
	if handleType.NumOut() != 2 {
		panic("Invalid query handle, func should return 2 parameters interface, error")
	}
	args := []int{}
	current := 0
	params := RouteParams{}
	for i := 0; i < handleType.NumIn(); i++ {

		if handleType.In(i) == reflect.TypeOf(ResolveParams{}) {
			args = append(args, ArgTypeParams)
		} else {
			switch current {
			case 0:
				args = append(args, ArgTypeSource)
				break
			case 1:
				args = append(args, ArgTypeArgs)
				break
			case 2:
				params.Context = handleType.In(i)
				args = append(args, ArgTypeContext)
				break
			}
			current++
		}
	}
	params.Handle = handle
	params.Args = args
	return params
}
func (r *Router) Query(path string, handle interface{}) {

	r.queries[path] = r.getRouteParams(handle)
}

type RouterError struct {
	Text string
}

type InvalidSourceError struct {
	RouterError
}
type NotFoundRoute struct {
	RouterError
}

func (e RouterError) Error() string {
	return e.Text
}
