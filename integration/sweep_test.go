//go:build integration

package integration

// The read sweep: every GET operation in a server's definitions, called
// against the running server with arguments resolved from the fixtures, and
// its answer decoded (or its file read). The bespoke tests prove the shapes the
// tools rely on field by field; the sweep proves the rest of the read surface
// answers in its documented status with something in it, and that what it
// sends lands in the model, and that it keeps doing so as the definitions
// change: a GET the importer adds is swept on the next run with nothing to
// write, and one that fails must be classified here.
//
// What the sweep holds an answer to: it decodes; it carries something (a
// file of at least one byte, JSON with at least one value that is not empty,
// zero or false), because an empty list decodes into any model and so proves
// nothing about its shape; and no object in it is lost whole, which is how a
// model whose fields do not match the server shows up (the JSON decoder drops
// a key it has no field for without a word). It does not check every field:
// a server sends many its document does not declare, and a model that holds
// one of an object's keys holds that object.
//
// Every operation either answers with something, or has a sweepCase that
// says why not: the feature needs something the container lacks (a tuner, a
// DLNA client, a transcoding session), the endpoint is gone from the server,
// the server answers a shape its document does not describe, or what the
// operation reads is not there on a fresh server. A Status, Decode or Empty
// case whose operation starts answering fails the sweep, so a stale one is
// noticed and removed, the way a stale importer workaround is. A Skip case
// is never called, because calling it would hang on ffmpeg, scan a network
// or need a fixture the container cannot have, so it is only ever reviewed by
// hand. Nothing is allowed to answer one way on some runs and another on
// others: where a server does, the test sets up what makes it answer the
// same way every time.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/katbyte/embyfin-mcp/internal/pandorest/config"
	"github.com/katbyte/embyfin-mcp/internal/pandorest/definitions"
	"github.com/katbyte/embyfin-mcp/lib/client"
)

// sweepCase is how the sweep treats one operation.
type sweepCase struct {
	// Skip leaves the operation uncalled, for the reason given.
	Skip string
	// Status is the error status the server answers with, and Decode is set
	// when it answers a body the documented model cannot decode; Why says
	// why that is the server's behaviour rather than a bug to fix.
	Status int
	Decode bool
	Why    string
	// Empty says why the operation answers with nothing on the fixtures (an
	// empty list, an object of zero values, a file of no bytes), which the
	// sweep otherwise fails.
	Empty string
	// Path and Options supply arguments by parameter name and options field,
	// beyond what the fixtures resolve.
	Path    map[string]string
	Options map[string]any
}

// sweepFixtures resolves arguments from the suite's fixtures. A path parameter
// is looked up as "<segment>/<param>", the literal path segment before the
// placeholder and the placeholder's name (Items/Id, Users/Id), then as the
// name alone; both case-insensitively. Required options are looked up by name
// in options; always holds options set whenever an operation has them (the
// user an API key has to name for anything user-scoped).
type sweepFixtures struct {
	path    map[string]string
	options map[string]string
	always  map[string]string
}

func (f sweepFixtures) resolvePath(path, param string) (string, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i, seg := range segments {
		if !strings.Contains(seg, "{"+param+"}") {
			continue
		}
		if i > 0 && !strings.Contains(segments[i-1], "{") {
			if v, ok := lookupFold(f.path, segments[i-1]+"/"+param); ok {
				return v, true
			}
		}
	}

	return lookupFold(f.path, param)
}

func lookupFold(m map[string]string, key string) (string, bool) {
	for k, v := range m {
		if strings.EqualFold(k, key) {
			return v, true
		}
	}

	return "", false
}

// sweep calls every GET operation of the service on sdk.
func sweep(t *testing.T, service string, sdk any, fixtures sweepFixtures, cases map[string]sweepCase) {
	t.Helper()

	cfg, ok := config.Find(service)
	if !ok {
		t.Fatalf("no service %q", service)
	}
	cfg, err := cfg.In("..").Resolve()
	if err != nil {
		t.Fatal(err)
	}
	svc, err := definitions.Load(cfg.Path(cfg.Definitions))
	if err != nil {
		t.Fatal(err)
	}

	gets := map[string]bool{}
	for _, op := range svc.Operations() {
		if op.Method != http.MethodGet {
			continue
		}
		gets[op.Name] = true
		c := cases[op.Name]

		t.Run(op.Name, func(t *testing.T) {
			if c.Skip != "" {
				t.Skip(c.Skip)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
			defer cancel()

			args, err := sweepArgs(ctx, sdk, op, fixtures, c)
			if err != nil {
				t.Fatalf("%s: %v; resolve it in the fixtures or give it a sweepCase", op.Key(), err)
			}
			status, decodeErr, answer, err := sweepCall(sdk, op, args)
			switch {
			case err == nil && (c.Status != 0 || c.Decode):
				t.Errorf("%s now answers; drop its sweepCase (%s)", op.Key(), c.Why)
			case err == nil:
				checkAnswer(t, op, c, answer)
			case c.Status != 0 && status == c.Status:
				t.Logf("%s: HTTP %d, as expected: %s", op.Key(), status, c.Why)
			case c.Decode && decodeErr:
				t.Logf("%s: does not decode, as expected: %s", op.Key(), c.Why)
			case decodeErr:
				t.Errorf("%s answers what its model cannot decode: %v", op.Key(), err)
			default:
				t.Errorf("%s: %v", op.Key(), err)
			}
		})
	}

	for name := range cases {
		if !gets[name] {
			t.Errorf("the sweepCase for %s names no GET operation", name)
		}
	}
}

// sweepArgs builds the call: the context, the path parameters, and an options
// struct with the required options and the case's options set.
func sweepArgs(ctx context.Context, sdk any, op *definitions.Operation, fixtures sweepFixtures, c sweepCase) ([]reflect.Value, error) {
	method := reflect.ValueOf(sdk).MethodByName(op.Name)
	if !method.IsValid() {
		return nil, errors.New("the SDK has no such method")
	}
	mt := method.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}

	for _, p := range op.PathParameters {
		value, ok := c.Path[p.Name]
		if !ok {
			value, ok = fixtures.resolvePath(op.Path, p.Name)
		}
		if !ok {
			return nil, fmt.Errorf("no value for path parameter {%s}", p.Name)
		}
		v := reflect.New(mt.In(len(args))).Elem()
		if err := setValue(v, value); err != nil {
			return nil, fmt.Errorf("{%s}: %w", p.Name, err)
		}
		args = append(args, v)
	}
	if op.Request != nil {
		return nil, errors.New("a GET with a request body")
	}

	if len(op.Options) > 0 {
		options := reflect.New(mt.In(len(args))).Elem()
		for _, o := range op.Options {
			value, ok := c.Options[o.Field]
			if !ok {
				value, ok = lookupFold(fixtures.always, o.Name)
			}
			if !ok && o.Required {
				value, ok = lookupFold(fixtures.options, o.Name)
				if !ok {
					return nil, fmt.Errorf("no value for required option %s", o.Name)
				}
			}
			if !ok {
				continue
			}
			if err := setValue(options.FieldByName(o.Field), value); err != nil {
				return nil, fmt.Errorf("option %s: %w", o.Name, err)
			}
		}
		args = append(args, options)
	}
	if len(args) != mt.NumIn() {
		return nil, fmt.Errorf("built %d arguments for a method that takes %d", len(args), mt.NumIn())
	}

	return args, nil
}

// setValue sets a path argument or options field from a fixture: a string, a
// bool, an int, or a list.
func setValue(v reflect.Value, value any) error {
	if !v.IsValid() {
		return errors.New("no such field")
	}
	s := fmt.Sprint(value)
	switch v.Kind() {
	case reflect.String:
		v.SetString(s)
	case reflect.Bool:
		b, err := strconv.ParseBool(s)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return err
		}
		v.SetInt(n)
	case reflect.Float32, reflect.Float64:
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		v.SetFloat(n)
	case reflect.Pointer:
		// a bool or a number: the options hold both as pointers, so 0 and
		// false can be asked for
		elem := reflect.New(v.Type().Elem())
		if err := setValue(elem.Elem(), value); err != nil {
			return fmt.Errorf("cannot set %s from %q: %w", v.Type(), s, err)
		}
		v.Set(elem)
	case reflect.Slice:
		parts := strings.Split(s, ",")
		list := reflect.MakeSlice(v.Type(), len(parts), len(parts))
		for i, part := range parts {
			if err := setValue(list.Index(i), part); err != nil {
				return err
			}
		}
		v.Set(list)
	default:
		return fmt.Errorf("cannot set a %s", v.Type())
	}

	return nil
}

// sweepCall calls the operation, reads what it answered, and classifies a
// failure: the status of a *client.StatusError, or a decode error (the server
// answered in a documented status, but not the documented shape).
func sweepCall(sdk any, op *definitions.Operation, args []reflect.Value) (status int, decodeErr bool, answer sweepAnswer, err error) {
	results := reflect.ValueOf(sdk).MethodByName(op.Name).Call(args)
	resp, _ := results[0].FieldByName("HttpResponse").Interface().(*http.Response)
	err, _ = results[1].Interface().(error)

	if err == nil && resp != nil && op.Response != nil {
		defer func() { _ = resp.Body.Close() }()
		// a file is streamed, so only its start is read; a JSON answer is
		// buffered by the client and left readable after it is decoded
		limit := int64(client.MaxResponseBytes)
		if op.Response.Type.Type == definitions.RawFile {
			limit = 1 << 20
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, limit))
		if readErr != nil {
			return 0, false, answer, fmt.Errorf("reading the answer: %w", readErr)
		}
		if op.Response.Type.Type == definitions.RawFile {
			answer.empty = len(body) == 0
		} else {
			answer = judgeJSON(body, results[0].FieldByName("Model"))
		}
	}
	if err == nil {
		return 0, false, answer, nil
	}
	if status = client.StatusCode(err); status != 0 {
		return status, false, answer, err
	}

	return 0, resp != nil, answer, err
}

// sweepAnswer is what the sweep makes of a successful answer.
type sweepAnswer struct {
	// empty is an answer with nothing in it: no bytes, or JSON holding no
	// value but empty ones, zeros and false
	empty bool
	// dropped are the places in the answer whose content the model lost
	// whole
	dropped []string
}

// checkAnswer holds a successful answer to what the sweep claims of it: that
// it carries something, unless its case says why not, and that the model kept
// what it carries.
func checkAnswer(t *testing.T, op *definitions.Operation, c sweepCase, a sweepAnswer) {
	t.Helper()

	switch {
	case a.empty && c.Empty == "":
		t.Errorf("%s answers with nothing, which decodes into any model and so proves only its status: point it at a fixture that has something, or give it an Empty case", op.Key())
	case !a.empty && c.Empty != "":
		t.Errorf("%s now answers with something; drop its Empty case (%s)", op.Key(), c.Empty)
	case a.empty:
		t.Logf("%s: answers with nothing, as expected: %s", op.Key(), c.Empty)
	}
	for _, d := range a.dropped {
		t.Errorf("%s: the model drops what the server sent at %s", op.Key(), d)
	}
}

// judgeJSON reads a JSON answer against the model it was decoded into.
func judgeJSON(body []byte, model reflect.Value) sweepAnswer {
	var answer any
	if err := json.Unmarshal(body, &answer); err != nil {
		// text the model holds whole, or nothing at all
		return sweepAnswer{empty: len(bytes.TrimSpace(body)) == 0}
	}
	a := sweepAnswer{empty: emptyJSON(answer)}
	if model.IsValid() {
		a.dropped = dropped(answer, model, "the answer")
		if len(a.dropped) > 5 {
			a.dropped = append(a.dropped[:5], fmt.Sprintf("%d more places", len(a.dropped)-5))
		}
	}

	return a
}

// emptyJSON reports whether a decoded JSON value holds nothing but empty
// strings, lists and objects, zeros, false and null.
func emptyJSON(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case bool:
		return !v
	case float64:
		return v == 0
	case string:
		return v == ""
	case []any:
		for _, e := range v {
			if !emptyJSON(e) {
				return false
			}
		}
		return true
	case map[string]any:
		for _, e := range v {
			if !emptyJSON(e) {
				return false
			}
		}
		return true
	}

	return false
}

// dropped walks a decoded JSON answer beside the model it was decoded into
// and names each place whose content the model lost whole: an object with
// something in it none of whose keys the model has a field for (the JSON
// decoder drops those without a word), or a list or map the model holds
// fewer entries of. A key the model lacks beside one it holds is not
// reported: servers send many fields their documents leave out.
func dropped(answer any, model reflect.Value, at string) []string {
	for model.Kind() == reflect.Pointer || model.Kind() == reflect.Interface {
		switch {
		case model.IsNil() && emptyJSON(answer):
			return nil
		case model.IsNil():
			return []string{at + " (the model holds nothing)"}
		case model.Kind() == reflect.Interface:
			return nil // a value of no declared type, held as it came
		}
		model = model.Elem()
	}

	var out []string
	switch a := answer.(type) {
	case map[string]any:
		keys := slices.Sorted(maps.Keys(a))
		switch model.Kind() {
		case reflect.Struct:
			fields := jsonFields(model.Type())
			var sent, kept []string
			for _, k := range keys {
				if emptyJSON(a[k]) {
					continue
				}
				sent = append(sent, k)
				i, ok := fields[strings.ToLower(k)]
				if !ok {
					continue
				}
				kept = append(kept, k)
				out = append(out, dropped(a[k], model.Field(i), at+"."+k)...)
			}
			if len(sent) > 0 && len(kept) == 0 {
				out = append(out, fmt.Sprintf("%s (the model has no field for any of %s)", at, strings.Join(sent, ", ")))
			}
		case reflect.Map:
			if model.Type().Key().Kind() != reflect.String {
				return nil
			}
			for _, k := range keys {
				v := model.MapIndex(reflect.ValueOf(k).Convert(model.Type().Key()))
				switch {
				case !v.IsValid() && !emptyJSON(a[k]):
					out = append(out, fmt.Sprintf("%s[%q] (missing from the model)", at, k))
				case v.IsValid():
					out = append(out, dropped(a[k], v, fmt.Sprintf("%s[%q]", at, k))...)
				}
			}
		default:
			// JSON held raw (json.RawMessage): nothing is dropped
		}
	case []any:
		// a byte slice is JSON held raw (json.RawMessage)
		if (model.Kind() != reflect.Slice && model.Kind() != reflect.Array) || model.Type().Elem().Kind() == reflect.Uint8 {
			return nil
		}
		if model.Len() < len(a) {
			return []string{fmt.Sprintf("%s (%d entries sent, %d held)", at, len(a), model.Len())}
		}
		for i, v := range a {
			out = append(out, dropped(v, model.Index(i), fmt.Sprintf("%s[%d]", at, i))...)
		}
	}

	return out
}

// jsonFields maps the lowercased JSON names of a struct's fields to their
// index, the way the JSON decoder matches keys (case-insensitively).
func jsonFields(t reflect.Type) map[string]int {
	fields := map[string]int{}
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		fields[strings.ToLower(name)] = i
	}

	return fields
}
