package main

import (
	"sync"

	"github.com/gorilla/websocket"
)

type User struct {
	Login     string
	SessionID string
	GameID    string
	Ready     bool
	Turn      bool
	Voted     bool
	InGame    bool
}

type Room struct {
	GameID  string
	started bool
	Users   []*User
}

type TextResponse struct {
	Text string `json:"text"`
}

func (u *User) setVoted(status bool) {
	u.Voted = status
}

func (u *User) setInGame(status bool) {
	u.InGame = status
}

var (
	mu      sync.Mutex
	clients = make(map[*websocket.Conn][]*Room)
)
