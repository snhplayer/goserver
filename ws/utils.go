package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"
)

func SerializeToString(msg proto.Message) ([]byte, error) {
	data, err := proto.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func addUserToRoom(room *Room, user *User) {
	room.Users = append(room.Users, user)
}

func removeUserFromRoom(room *Room, sessionID string) {
	for i, user := range room.Users {
		if user.SessionID == sessionID {
			room.Users = append(room.Users[:i], room.Users[i+1:]...)
			break
		}
	}
}

func clientsMoved(gameID string) int {
	var turnCount int

	mu.Lock()
	defer mu.Unlock()

	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				for _, user := range room.Users {
					if user.Turn {
						turnCount++
					}
				}
			}
		}
	}

	return turnCount
}

func ClientsInGame(gameID string) int {
	var clientsCount int

	mu.Lock()
	defer mu.Unlock()

	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				for _, user := range room.Users {
					if user.InGame {
						log.Printf("Client in game: User: %v", user)
						clientsCount++
					}
				}
			}
		}
	}

	log.Printf("Total clients in game: %d", clientsCount)
	return clientsCount
}

func ClientsInRoom(gameID string) int {
	var count int

	mu.Lock()
	defer mu.Unlock()

	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				count += len(room.Users)
			}
		}
	}

	return count
}

func clientsReady(gameID string) int {
	var readyCount int

	mu.Lock()
	defer mu.Unlock()

	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				for _, user := range room.Users {
					if user.Ready {
						log.Printf("Client ready: User: %v", user)
						readyCount++
					}
				}
			}
		}
	}

	log.Printf("Total clients ready: %d", readyCount)
	return readyCount
}

func GetText(gameID string) (string, error) {
	url := "http://localhost:8080/text"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		log.Printf("Error creating request for game_id %s: %v", gameID, err)
		return "", err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Request failed for game_id %s: %v", gameID, err)
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Error for game_id %s: Status Code %d", gameID, resp.StatusCode)
		return "", fmt.Errorf("status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body for game_id %s: %v", gameID, err)
		return "", err
	}

	var data TextResponse
	if err := json.Unmarshal(body, &data); err != nil {
		log.Printf("Error unmarshaling JSON for game_id %s: %v", gameID, err)
		return "", err
	}

	if data.Text == "" {
		log.Printf("Error for game_id %s: No text received", gameID)
		return "", fmt.Errorf("no text received")
	}

	log.Printf("Received text for game_id %s: %s", gameID, data.Text)
	return data.Text, nil
}

func disconnectUserFromDB(sessionID string) error {
	url := "http://localhost:8080/disconnect"

	data := map[string]string{"session_id": sessionID}

	jsonData, err := json.Marshal(data)
	if err != nil {
		log.Printf("Error marshalling JSON: %v", err)
		return err
	}


	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		log.Printf("Error creating request: %v", err)
		return err
	}
	req.Header.Set("Content-Type", "application/json")


	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Request failed for session_id %s: %v", sessionID, err)
		return err
	}
	defer resp.Body.Close()


	if resp.StatusCode == http.StatusOK {
		log.Println("Request was successful.")

		var response map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			log.Printf("Error decoding response JSON: %v", err)
			return err
		}
		log.Printf("Response JSON: %v", response)
	} else {
		log.Printf("Request failed. Status Code: %d", resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading response body: %v", err)
			return err
		}
		log.Printf("Response Text: %s", body)
	}

	return nil
}

func disconnectUser(sessionID string) {
	log.Printf("Disconnecting user with session_id: %s", sessionID)

	mu.Lock()
	defer mu.Unlock()

	found := false

	for conn, rooms := range clients {
		for _, room := range rooms {
			for i, user := range room.Users {
				if user.SessionID == sessionID {
					log.Printf("Found user to disconnect: %v", user)

					room.Users = append(room.Users[:i], room.Users[i+1:]...)
					log.Printf("User removed from clients")

					if err := conn.Close(); err != nil {
						log.Printf("Error closing WebSocket connection: %v", err)
					} else {
						log.Printf("WebSocket connection closed for user: %s", user.Login)
					}

					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if found {
			break
		}
	}

	if !found {
		log.Printf("User with session_id %s not found in clients", sessionID)
	}
}

func deleteUser(sessionID string) error {
	url := "http://localhost:8080/exit"

	data := map[string]string{"session_id": sessionID}
	payload, err := json.Marshal(data)
	if err != nil {
		log.Printf("Failed to marshal request payload: %v", err)
		return err
	}

	resp, err := http.Post(url, "application/json", bytes.NewBuffer(payload))
	if err != nil {
		log.Printf("Request failed for session_id %s: %v", sessionID, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Println("Request was successful.")

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Failed to read response body: %v", err)
			return err
		}

		var responseJSON map[string]interface{}
		if err := json.Unmarshal(body, &responseJSON); err != nil {
			log.Printf("Failed to unmarshal response JSON: %v", err)
			return err
		}

		log.Printf("Response JSON: %v", responseJSON)

		mu.Lock()
		for _, rooms := range clients {
			for _, room := range rooms {
				for i, user := range room.Users {
					if user.SessionID == sessionID {
						room.Users = append(room.Users[:i], room.Users[i+1:]...)
						break
					}
				}
			}
		}
		mu.Unlock()
	} else {
		log.Printf("Request failed.")
		log.Printf("Status Code: %d", resp.StatusCode)

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Failed to read response body: %v", err)
			return err
		}
		log.Printf("Response Text: %s", body)
	}

	return nil
}

func updateGame(game_id string, senderWebSocket *websocket.Conn) {
	log.Printf("Updating game after disconecting user")
	if ClientsInGame(game_id) == clientsMoved(game_id) {
		SendDeleteMessage(string(game_id))
		mu.Lock()
		for _, rooms := range clients {
			for _, room := range rooms {
				if room.GameID == string(game_id) {
					for _, user := range room.Users {
						user.Turn = false
						user.Voted = false
					}
				}
			}
		}
		mu.Unlock()
	}

	if ClientsInRoom(game_id) == clientsReady(game_id) {
		text, err := GetText(string(game_id))
		if err != nil {
			log.Printf("Error fetching text for game_id %s: %v", game_id, err)
			return
		}
		SendStartGameMessage(string(game_id), text)

		mu.Lock()
		for _, rooms := range clients {
			for _, room := range rooms {
				if room.GameID == string(game_id) {
					for _, user := range room.Users {
						user.Ready = false
						sendUserStatus(user.SessionID, user.GameID, senderWebSocket)
					}
				}
			}
		}
		mu.Unlock()
	}
}
