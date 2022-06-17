// Copyright 2017 Amazon.com, Inc. or its affiliates. All Rights Reserved.
/**
 * mostly taken from aws lambda go stub for
 * best compatibility. This will have to change
 * to allow usage of WAS protocol and
 */
package functions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/fcgi"
	"reflect"
)

type Handler interface {
	Invoke(ctx context.Context, payload []byte) ([]byte, error)
}

type server struct {
	Handler
	baseContext              context.Context
	jsonResponseEscapeHTML   bool
	jsonResponseIndentPrefix string
	jsonResponseIndentValue  string
}

func (s server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "can't read body", http.StatusBadRequest)
		return
	}

	data, err := s.Invoke(s.baseContext, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(data)
}

type bytesHandlerFunc func(context.Context, []byte) ([]byte, error)

func (h bytesHandlerFunc) Invoke(ctx context.Context, payload []byte) ([]byte, error) {
	return h(ctx, payload)
}

func errorHandler(err error) Handler {
	return bytesHandlerFunc(func(_ context.Context, _ []byte) ([]byte, error) {
		return nil, err
	})
}

func validateArguments(handler reflect.Type) (bool, error) {
	handlerTakesContext := false
	if handler.NumIn() > 2 {
		return false, fmt.Errorf("handlers may not take more than two arguments, but handler takes %d", handler.NumIn())
	} else if handler.NumIn() > 0 {
		contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
		argumentType := handler.In(0)
		handlerTakesContext = argumentType.Implements(contextType)
		if handler.NumIn() > 1 && !handlerTakesContext {
			return false, fmt.Errorf("handler takes two arguments, but the first is not Context. got %s", argumentType.Kind())
		}
	}

	return handlerTakesContext, nil
}

func validateReturns(handler reflect.Type) error {
	errorType := reflect.TypeOf((*error)(nil)).Elem()

	switch n := handler.NumOut(); {
	case n > 2:
		return fmt.Errorf("handler may not return more than two values")
	case n > 1:
		if !handler.Out(1).Implements(errorType) {
			return fmt.Errorf("handler returns two values, but the second does not implement error")
		}
	case n == 1:
		if !handler.Out(0).Implements(errorType) {
			return fmt.Errorf("handler returns a single value, but it does not implement error")
		}
	}

	return nil
}

func reflectHandler(handlerFunc interface{}, s *server) Handler {

	if handlerFunc == nil {
		return errorHandler(errors.New("handler is nil"))
	}

	if handler, ok := handlerFunc.(Handler); ok {
		return handler
	}

	handler := reflect.ValueOf(handlerFunc)
	handlerType := reflect.TypeOf(handlerFunc)
	if handlerType.Kind() != reflect.Func {
		return errorHandler(fmt.Errorf("handler kind %s is not %s", handlerType.Kind(), reflect.Func))
	}

	takesContext, err := validateArguments(handlerType)
	if err != nil {
		return errorHandler(err)
	}

	if err := validateReturns(handlerType); err != nil {
		return errorHandler(err)
	}

	return bytesHandlerFunc(func(ctx context.Context, payload []byte) ([]byte, error) {
		in := bytes.NewBuffer(payload)
		out := bytes.NewBuffer(nil)
		decoder := json.NewDecoder(in)
		encoder := json.NewEncoder(out)
		encoder.SetEscapeHTML(s.jsonResponseEscapeHTML)
		encoder.SetIndent(s.jsonResponseIndentPrefix, s.jsonResponseIndentValue)

		// construct arguments
		var args []reflect.Value
		if takesContext {
			args = append(args, reflect.ValueOf(ctx))
		}
		if (handlerType.NumIn() == 1 && !takesContext) || handlerType.NumIn() == 2 {
			eventType := handlerType.In(handlerType.NumIn() - 1)
			event := reflect.New(eventType)
			if err := decoder.Decode(event.Interface()); err != nil {
				return nil, err
			}
			args = append(args, event.Elem())
		}

		response := handler.Call(args)

		// return the error, if any
		if len(response) > 0 {
			if errVal, ok := response[len(response)-1].Interface().(error); ok && errVal != nil {
				return nil, errVal
			}
		}
		// set the response value, if any
		var val interface{}
		if len(response) > 1 {
			val = response[0].Interface()
		}
		if err := encoder.Encode(val); err != nil {
			return nil, err
		}

		responseBytes := out.Bytes()
		// back-compat, strip the encoder's trailing newline unless WithSetIndent was used
		if s.jsonResponseIndentValue == "" && s.jsonResponseIndentPrefix == "" {
			return responseBytes[:len(responseBytes)-1], nil
		}

		return responseBytes, nil
	})
}

func newServer(handlerFunc interface{}) *server {

	s := &server{
		baseContext:              context.Background(),
		jsonResponseEscapeHTML:   false,
		jsonResponseIndentPrefix: "",
		jsonResponseIndentValue:  "",
	}
	s.Handler = reflectHandler(handlerFunc, s)
	return s
}

func Start(handler interface{}) {
	fcgi.Serve(nil, newServer(handler))
}
