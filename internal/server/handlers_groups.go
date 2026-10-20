package server

import (
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/bradyloveland/taper/internal/store"
)

// Group chats are made by admins and the board, with any name and anyone in
// them. Leaders look after them; their members talk in them.

type groupFormData struct {
	A          chanAccess // empty for a new group
	Members    []*store.User
	Candidates []candidate
	Name       string
	Error      string
}

// groupCandidates lists the active people who could be added, leaving out
// those already in.
func (s *Server) groupCandidates(skip map[int64]bool) ([]candidate, error) {
	users, err := s.store.ListUsers(store.UserFilter{Status: "active"})
	if err != nil {
		return nil, err
	}
	list := candidates(users, skip)
	sort.SliceStable(list, func(i, j int) bool {
		return strings.ToLower(list[i].DisplayName) < strings.ToLower(list[j].DisplayName)
	})
	return list, nil
}

// chosenPeople reads the people ticked in a picker, keeping active accounts.
func (s *Server) chosenPeople(r *http.Request) []int64 {
	_ = r.ParseForm()
	var ids []int64
	seen := map[int64]bool{}
	for _, v := range r.PostForm["user"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || seen[id] {
			continue
		}
		if u, err := s.store.GetUser(id); err == nil && u.Active {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	return ids
}

func (s *Server) handleGroupNewForm(w http.ResponseWriter, r *http.Request) {
	list, err := s.groupCandidates(map[int64]bool{current(r).user.ID: true})
	if err != nil {
		s.serverError(w, r, "listing people", err)
		return
	}
	s.render(w, r, http.StatusOK, "group-form", "New group chat", "chat", groupFormData{Candidates: list})
}

func (s *Server) handleGroupCreate(w http.ResponseWriter, r *http.Request) {
	u := current(r).user
	name, msg := cleanName(r.PostFormValue("name"), "name for the chat")
	members := append(s.chosenPeople(r), u.ID)
	if msg == "" && len(members) < 2 {
		msg = "Choose at least one person to chat with."
	}
	if msg != "" {
		list, _ := s.groupCandidates(map[int64]bool{u.ID: true})
		s.render(w, r, http.StatusUnprocessableEntity, "group-form", "New group chat", "chat",
			groupFormData{Candidates: list, Name: r.PostFormValue("name"), Error: msg})
		return
	}
	ch, err := s.store.CreateGroup(name, u.ID, members)
	if err != nil {
		s.serverError(w, r, "creating group chat", err)
		return
	}
	slog.Info("group chat created", "by", u.Username, "chat", ch.ID, "members", len(members))
	s.redirect(w, r, chatPath(ch.ID), name+" is ready, with "+people(len(members))+" in it.")
}

// group loads the group chat in the URL for a leader to look after.
func (s *Server) group(w http.ResponseWriter, r *http.Request) (chanAccess, bool) {
	a, ok := s.channel(w, r)
	if !ok {
		return a, false
	}
	if a.Ch.Kind != store.ChannelGroup {
		s.notFound(w, r)
		return a, false
	}
	if !a.Manage {
		s.renderError(w, r, http.StatusForbidden, "Admins and the board only", "Only admins and board members look after group chats.")
		return a, false
	}
	return a, true
}

func (s *Server) handleGroupPeople(w http.ResponseWriter, r *http.Request) {
	a, ok := s.group(w, r)
	if !ok {
		return
	}
	s.renderGroupPeople(w, r, http.StatusOK, a, "")
}

func (s *Server) renderGroupPeople(w http.ResponseWriter, r *http.Request, status int, a chanAccess, msg string) {
	members, err := s.store.ChannelMembers(a.Ch.ID)
	if err != nil {
		s.serverError(w, r, "listing members", err)
		return
	}
	in := map[int64]bool{}
	for _, m := range members {
		in[m.ID] = true
	}
	list, err := s.groupCandidates(in)
	if err != nil {
		s.serverError(w, r, "listing people", err)
		return
	}
	s.render(w, r, status, "group-people", "People in "+a.Name, "chat",
		groupFormData{A: a, Members: members, Candidates: list, Name: a.Ch.Name, Error: msg})
}

func (s *Server) handleGroupAdd(w http.ResponseWriter, r *http.Request) {
	a, ok := s.group(w, r)
	if !ok {
		return
	}
	ids := s.chosenPeople(r)
	if len(ids) == 0 {
		s.renderGroupPeople(w, r, http.StatusUnprocessableEntity, a, "Choose at least one person to add.")
		return
	}
	n, err := s.store.AddChannelMembers(a.Ch.ID, ids)
	if err != nil {
		s.serverError(w, r, "adding to group chat", err)
		return
	}
	s.redirect(w, r, chatPath(a.Ch.ID)+"/people", people(n)+" added.")
}

func (s *Server) handleGroupRemove(w http.ResponseWriter, r *http.Request) {
	a, ok := s.group(w, r)
	if !ok {
		return
	}
	uid, err := strconv.ParseInt(r.PathValue("uid"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	if err := s.store.RemoveChannelMember(a.Ch.ID, uid); err != nil {
		s.serverError(w, r, "removing from group chat", err)
		return
	}
	name := "They"
	if u, err := s.store.GetUser(uid); err == nil {
		name = u.DisplayName
	}
	s.redirect(w, r, chatPath(a.Ch.ID)+"/people", name+" is no longer in "+a.Name+".")
}

func (s *Server) handleGroupRename(w http.ResponseWriter, r *http.Request) {
	a, ok := s.group(w, r)
	if !ok {
		return
	}
	name, msg := cleanName(r.PostFormValue("name"), "name for the chat")
	if msg != "" {
		s.renderGroupPeople(w, r, http.StatusUnprocessableEntity, a, msg)
		return
	}
	if err := s.store.RenameChannel(a.Ch.ID, name); err != nil {
		s.serverError(w, r, "renaming group chat", err)
		return
	}
	s.redirect(w, r, chatPath(a.Ch.ID)+"/people", "Renamed to "+name+".")
}

func (s *Server) handleGroupDelete(w http.ResponseWriter, r *http.Request) {
	a, ok := s.group(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(r.PostFormValue("confirm")) != a.Ch.Name {
		s.renderGroupPeople(w, r, http.StatusUnprocessableEntity, a, "Type the chat's name exactly to delete it.")
		return
	}
	if err := s.store.DeleteChannel(a.Ch.ID); err != nil {
		s.serverError(w, r, "deleting group chat", err)
		return
	}
	s.cleanFiles()
	slog.Info("group chat deleted", "by", current(r).user.Username, "chat", a.Ch.ID, "name", a.Ch.Name)
	s.redirect(w, r, "/chat", a.Ch.Name+" is deleted, with its messages.")
}
