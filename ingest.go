package main

import (
	"github.com/steamvogue/wstat/internal/fpm"
	"github.com/steamvogue/wstat/internal/logsrc"
	"github.com/steamvogue/wstat/internal/parser"
	"github.com/steamvogue/wstat/internal/store"
)

type ingestionRouter struct {
	services map[string]*fpm.AccessParser
	poller   *fpm.Poller
}

func newIngestionRouter() *ingestionRouter {
	return &ingestionRouter{services: map[string]*fpm.AccessParser{}}
}
func (r *ingestionRouter) ingest(st *store.Store, line logsrc.RawLine) {
	if line.Source.Kind == logsrc.FPM {
		ap := r.services[line.Source.Path]
		if r.poller == nil {
			return
		}
		if ap == nil {
			r.poller.AddBadAccess(line.Source.Path)
			return
		}
		event, ok := ap.Parse(line.Text, line.Source.Vhost)
		if !ok {
			r.poller.AddBadAccess(line.Source.Path)
			return
		}
		r.poller.AddAccess(line.Source.Path, event, line.Seeded)
		return
	}
	event, ok := parser.Parse(line.Text, line.Source.Vhost)
	if !ok {
		st.AddBad()
		return
	}
	if line.Seeded {
		st.AddSeed(event)
	} else {
		st.Add(event)
	}
}
