//go:build integration

package integration

// The read sweep: every GET operation in a server's definitions, called
// against the running server with arguments resolved from the fixtures, and
// its answer decoded (or its file read). The bespoke tests prove the shapes the
// tools rely on field by field; the sweep proves the rest of the read surface
// answers in its documented status and shape, and that it keeps doing so as
// the definitions change: a GET the importer adds is swept on the next run
// with nothing to write, and one that fails must be classified here.
//
// Every operation either answers, or has a sweepCase that says why not: the
// feature needs something the container lacks (a tuner, a DLNA client, a
// transcoding session), the endpoint is gone from the server, or the server
// answers a shape its document does not describe. A case whose operation
// starts answering fails the sweep, so a stale case is noticed and removed,
// the way a stale importer workaround is.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

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
	// Sometimes accepts an answer as well as the Status, for a call that
	// depends on something outside the container (a catalogue fetched
	// through the provider proxy).
	Sometimes bool
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

	svc, err := definitions.Load(filepath.Join("..", "api-definitions", service))
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
			status, decodeErr, err := sweepCall(sdk, op, args)
			switch {
			case err == nil && (c.Status != 0 || c.Decode) && !c.Sometimes:
				t.Errorf("%s now answers; drop its sweepCase (%s)", op.Key(), c.Why)
			case err == nil:
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
		b, err := strconv.ParseBool(s)
		if err != nil || v.Type().Elem().Kind() != reflect.Bool {
			return fmt.Errorf("cannot set %s from %q", v.Type(), s)
		}
		v.Set(reflect.ValueOf(&b))
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

// sweepCall calls the operation, reads a streamed file, and classifies a
// failure: the status of a *client.StatusError, or a decode error (the server
// answered in a documented status, but not the documented shape).
func sweepCall(sdk any, op *definitions.Operation, args []reflect.Value) (status int, decodeErr bool, err error) {
	results := reflect.ValueOf(sdk).MethodByName(op.Name).Call(args)
	resp, _ := results[0].FieldByName("HttpResponse").Interface().(*http.Response)
	err, _ = results[1].Interface().(error)

	if err == nil && resp != nil && op.Response != nil && op.Response.Type.Type == definitions.RawFile {
		defer func() { _ = resp.Body.Close() }()
		if _, readErr := io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20)); readErr != nil {
			return 0, false, fmt.Errorf("reading the file: %w", readErr)
		}
	}
	if err == nil {
		return 0, false, nil
	}
	if status = client.StatusCode(err); status != 0 {
		return status, false, err
	}

	return 0, resp != nil, err
}
