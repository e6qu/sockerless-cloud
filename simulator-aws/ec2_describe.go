package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ec2DescribeEach answers a Describe call with the resources the request names
// under param, refusing one that does not exist, or with every resource when
// it names none, sorted by ID.
func ec2DescribeEach[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], param, notFoundCode, notFoundFormat string, id, fieldsXML func(T) string, action, set string) {
	ids := ec2ParamList(r, param)
	results := make([]T, 0)
	if len(ids) > 0 {
		for _, want := range ids {
			item, ok := store.Get(want)
			if !ok {
				ec2ErrorXML(w, notFoundCode, fmt.Sprintf(notFoundFormat, want), http.StatusBadRequest)
				return
			}
			results = append(results, item)
		}
	} else {
		results = append(results, store.List()...)
	}
	sort.Slice(results, func(i, j int) bool { return id(results[i]) < id(results[j]) })
	ec2WriteResponse(w, action, "<"+set+">"+ec2ItemsXML(results, fieldsXML)+"</"+set+">")
}

// ec2DescribeMatching answers a Describe call with the resources, sorted by
// ID, that the request names under param (or all of them) and that match its
// filters, refusing a named resource that does not exist.
func ec2DescribeMatching[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], param, notFoundCode, notFoundFormat string, id func(T) string, matches func(T, map[string][]string) bool, fieldsXML func(T) string, action, set string) {
	ids := ec2ParamList(r, param)
	for _, want := range ids {
		if _, ok := store.Get(want); !ok {
			ec2ErrorXML(w, notFoundCode, fmt.Sprintf(notFoundFormat, want), http.StatusBadRequest)
			return
		}
	}
	filters := ec2Filters(r)
	list := store.List()
	sort.Slice(list, func(i, j int) bool { return id(list[i]) < id(list[j]) })
	results := make([]T, 0, len(list))
	for _, item := range list {
		if len(ids) > 0 && !ec2StrInValues(id(item), ids) {
			continue
		}
		if matches(item, filters) {
			results = append(results, item)
		}
	}
	ec2WriteResponse(w, action, "<"+set+">"+ec2ItemsXML(results, fieldsXML)+"</"+set+">")
}

// ec2DescribePaged answers a Describe call with the resources the request
// names under param, or all of them, a NextToken page at a time.
func ec2DescribePaged[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], param string, id, fieldsXML func(T) string, action, set string) {
	ids := ec2ParamList(r, param)
	results := make([]T, 0)
	for _, item := range store.List() {
		if len(ids) > 0 && !ec2StrInValues(id(item), ids) {
			continue
		}
		results = append(results, item)
	}
	results, nextToken, pageOK := awsKeyPage(w, ec2BadToken, results, id, r.FormValue("NextToken"), ec2AtoiOr(r.FormValue("MaxResults"), 0), 0)
	if !pageOK {
		return
	}
	ec2WriteResponse(w, action, "<"+set+">"+ec2ItemsXML(results, fieldsXML)+"</"+set+">"+ec2NextTokenXML(nextToken))
}

func ec2ItemsXML[T any](items []T, fieldsXML func(T) string) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString("<item>")
		b.WriteString(fieldsXML(item))
		b.WriteString("</item>")
	}
	return b.String()
}

// ec2NamedOrMatching returns the resources the request names under param,
// refusing one that does not exist, or every resource that matches its filters
// when it names none.
func ec2NamedOrMatching[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], param, notFoundCode, notFoundFormat string, matches func(T, map[string][]string) bool) ([]T, bool) {
	ids := ec2ParamList(r, param)
	var out []T
	if len(ids) > 0 {
		for _, want := range ids {
			item, ok := store.Get(want)
			if !ok {
				ec2ErrorXML(w, notFoundCode, fmt.Sprintf(notFoundFormat, want), http.StatusBadRequest)
				return nil, false
			}
			out = append(out, item)
		}
		return out, true
	}
	filters := ec2Filters(r)
	for _, item := range store.List() {
		if matches(item, filters) {
			out = append(out, item)
		}
	}
	return out, true
}
