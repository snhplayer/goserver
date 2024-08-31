package main

import (
	"fmt"
	"log"
	"sync"

	"github.com/gorilla/websocket"
	"google.golang.org/protobuf/proto"

	game "ws_server/proto"
)

func handleClient(conn *websocket.Conn) {
	log.Println("Client connected")
	defer func() {
		mu.Lock()
		delete(clients, conn)
		mu.Unlock()
		conn.Close()
		log.Println("Client disconnected")
	}()

	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("Error reading message: %v", err)
			}
			break
		}

		var baseMsg game.BaseMessage
		if err := proto.Unmarshal(message, &baseMsg); err != nil {
			log.Printf("Error unmarshaling message: %v", err)
			continue
		}

		switch baseMsg.ClassId {
		case game.ClassTypes_PROTO_TYPE_USERINFO:
			handleUserInfo(conn, baseMsg.Data)
		case game.ClassTypes_PROTO_TYPE_ACTION:
			handleAction(conn, baseMsg.Data)
		case game.ClassTypes_PROTO_TYPE_STATUS:
			handleStatus(conn, baseMsg.Data)
		case game.ClassTypes_PROTO_TYPE_CHOOSE:
			handleChoose(conn, baseMsg.Data)
		case game.ClassTypes_PROTO_TYPE_GAMEINFO:
			handleGameInfo(baseMsg.Data)
		case game.ClassTypes_PROTO_TYPE_DISCONNECT:
			handleDisconnect(conn, baseMsg.Data)
		case game.ClassTypes_PROTO_TYPE_CHATMESSAGE:
			handleChatMessage(conn, baseMsg.Data)
		default:
			log.Printf("Unknown message type: %v", baseMsg.ClassId)
		}
	}
}

func handleChatMessage(conn *websocket.Conn, data []byte) {
	var chatMsg game.ChatMessage
	if err := proto.Unmarshal(data, &chatMsg); err != nil {
		log.Printf("Error unmarshaling chat message: %v", err)
		return
	}

	log.Printf("Received chat message from %s: %s", string(chatMsg.User.Login), string(chatMsg.Message))
	sendChatMessage(conn, string(chatMsg.User.GameId), string(chatMsg.User.Login), string(chatMsg.Message))
}

func handleUserInfo(conn *websocket.Conn, data []byte) {
	var userInfo game.UserInfo
	if err := proto.Unmarshal(data, &userInfo); err != nil {
		log.Printf("Error unmarshaling UserInfo: %v", err)
		return
	}

	log.Printf("Received user info: %v", userInfo)

	mu.Lock()
	defer mu.Unlock()

	if _, ok := clients[conn]; !ok {
		clients[conn] = []*Room{}
	}

	roomExists := false
	for _, room := range clients[conn] {
		if room.GameID == string(userInfo.User.GameId) {
			roomExists = true
			if userInfo.Connected {
				addUserToRoom(room, &User{
					Login:     string(userInfo.User.Login),
					SessionID: string(userInfo.User.SessionId),
				})
			} else {
				removeUserFromRoom(room, string(userInfo.User.SessionId))
				deleteUser(string(userInfo.User.SessionId))
			}
			break
		}
	}

	if !roomExists {
		newRoom := &Room{
			GameID:  string(userInfo.User.GameId),
			started: false,
			Users:   []*User{},
		}
		if userInfo.Connected {
			addUserToRoom(newRoom, &User{
				Login:     string(userInfo.User.Login),
				SessionID: string(userInfo.User.SessionId),
			})
		}
		clients[conn] = append(clients[conn], newRoom)
	}

	log.Printf("Current clients: %+v", clients)
	SendUserInfoToGameClients(&userInfo, conn)
	SendUpdateMessage(string(userInfo.User.Login), string(userInfo.User.SessionId), string(userInfo.User.GameId), conn)
}

func handleAction(conn *websocket.Conn, data []byte) {
	var action game.Action
	if err := proto.Unmarshal(data, &action); err != nil {
		log.Printf("Error unmarshaling Action: %v", err)
		return
	}
	log.Printf("Received action from: %s and game_id: %s", action.User.SessionId, action.User.GameId)

	mu.Lock()
	userTurned := false
	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == string(action.User.GameId) {
				for _, user := range room.Users {
					if user.SessionID == string(action.User.SessionId) {
						if user.Turn {
							userTurned = true
						} else {
							user.Turn = true
						}
						break
					}
				}
			}
		}
	}
	mu.Unlock()

	log.Printf("Current clients: %+v", clients)

	if userTurned {
		log.Printf("User already turned")
		return
	}

	if err := SendActionToGameClients(&action, conn); err != nil {
		log.Printf("Failed to send action to game clients: %v", err)
	}

	usersMoved := clientsMoved(string(action.User.GameId))
	users := ClientsInGame(string(action.User.GameId))
	log.Printf("users_moved: %d", usersMoved)
	log.Printf("users: %d", users)
	if usersMoved == users {
		SendDeleteMessage(string(action.User.GameId))
		mu.Lock()
		for _, rooms := range clients {
			for _, room := range rooms {
				if room.GameID == string(action.User.GameId) {
					for _, user := range room.Users {
						room.started = false
						user.Voted = false
					}
				}
			}
		}
		mu.Unlock()
	}
}

func handleStatus(conn *websocket.Conn, data []byte) {
	var status game.Ready
	if err := proto.Unmarshal(data, &status); err != nil {
		log.Printf("Error unmarshaling Ready: %v", err)
		return
	}
	log.Printf("Received status: %+v", status)

	mu.Lock()
	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == string(status.User.GameId) && !room.started {
				for _, user := range room.Users {
					if user.SessionID == string(status.User.SessionId) {
						user.Ready = !user.Ready
						status.Status = user.Ready
						log.Printf("Updated user ready status: %+v", user)
						break
					}
				}
			}
		}
	}
	mu.Unlock()

	users := ClientsInRoom(string(status.User.GameId))
	readyUsers := clientsReady(string(status.User.GameId))
	log.Printf("Clients ready in room %s: %d", status.User.GameId, readyUsers)
	log.Printf("Clients in room %s: %d", status.User.GameId, users)

	if err := SendStatusToGameClients(&status, conn); err != nil {
		log.Printf("Failed to send status to game clients: %v", err)
	}

	if readyUsers == users {
		text, err := GetText(string(status.User.GameId))
		if err != nil {
			log.Printf("Error fetching text for game_id %s: %v", status.User.GameId, err)
			return
		}
		SendStartGameMessage(string(status.User.GameId), text)
		mu.Lock()
		for _, rooms := range clients {
			for _, room := range rooms {
				if room.GameID == string(status.User.GameId) {
					for _, user := range room.Users {
						user.Ready = false
						user.Turn = false
						room.started = true
						status.Status = false
					}
				}
			}
		}
		mu.Unlock()

		if err := SendStatusToGameClients(&status, conn); err != nil {
			log.Printf("Failed to send status to game clients: %v", err)
		}
	}
}

func handleChoose(conn *websocket.Conn, data []byte) {
	var choose game.Choose
	if err := proto.Unmarshal(data, &choose); err != nil {
		log.Printf("Error unmarshaling Choose: %v", err)
		return
	}
	//log.Printf("Received choose: %+v", choose)
	log.Printf("Received session_id: %s", string(choose.User.SessionId))
	log.Printf("Received game_id: %s", string(choose.User.GameId))
	log.Printf("Received chosen_id: %s", string(choose.ChosenId))

	mu.Lock()
	defer mu.Unlock()

	userVoted := false
	for _, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == string(choose.User.GameId) {
				for _, user := range room.Users {
					if user.SessionID == string(choose.User.SessionId) {
						if user.Voted {
							userVoted = true
						} else {
							user.setVoted(true)
						}
						break
					}
				}
				if userVoted {
					break
				}
			}
		}
	}

	if userVoted {
		log.Println("User already voted, not sending chosen_id")
		return
	}

	if err := sendChosenID(&choose, conn); err != nil {
		log.Printf("Error sending chosen_id: %v", err)
	} else {
		log.Println("Sending chosen_id to clients")
	}
}

func handleGameInfo(data []byte) {
	var gameInfo game.GameInfo
	if err := proto.Unmarshal(data, &gameInfo); err != nil {
		log.Printf("Error unmarshaling GameInfo: %v", err)
		return
	}
	log.Println("Received game info: ")
	log.Printf("Received session_id: %s", gameInfo.User.SessionId)
	log.Printf("Received destinationId: %s", gameInfo.DestinationId)
	log.Printf("Received login: %s", gameInfo.User.Login)

	if err := sendUpdateInfoToClient(&gameInfo); err != nil {
		log.Printf("Error sending update info to client: %v", err)
	}
}

func handleDisconnect(conn *websocket.Conn, data []byte) {
	var disconnect game.Disconnect
	if err := proto.Unmarshal(data, &disconnect); err != nil {
		log.Printf("Error unmarshaling Disconnect: %v", err)
		return
	}
	log.Println("Received disconnect")

	login := string(disconnect.User.Login)
	sessionID := string(disconnect.User.SessionId)
	gameID := string(disconnect.User.GameId)

	log.Printf("Disconnect user %s", login)
	log.Printf("session id to disconnect: %s", sessionID)

	disconnectUser(sessionID)

	if err := disconnectUserFromDB(sessionID); err != nil {
		log.Printf("Error disconnecting user from DB: %v", err)
	}

	if err := sendUserDisconnectMessage(login, sessionID, gameID); err != nil {
		log.Printf("Error sending user disconnect message: %v", err)
	}

	log.Printf("Game id to update: %s", gameID)
	updateGame(gameID, conn)
}

func SendMessageToClient(client *websocket.Conn, serializedMessage []byte) error {
	if client == nil {
		return fmt.Errorf("client is nil")
	}

	err := client.WriteMessage(websocket.BinaryMessage, serializedMessage)
	if err != nil {
		log.Printf("Error sending message to client: %v", err)
		return err
	}

	log.Println("Message sent to client")
	return nil
}

func SendStatusToGameClients(status *game.Ready, senderWebSocket *websocket.Conn) error {
	gameID := string(status.User.GameId)
	log.Printf("Sending status to game clients for game_id %s", gameID)

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_STATUS,
	}

	statusData, err := SerializeToString(status)
	if err != nil {
		log.Printf("Error serializing status: %v", err)
		return err
	}
	baseMessage.Data = statusData

	serializedBaseMessage, err := SerializeToString(baseMessage)
	if err != nil {
		log.Printf("Error serializing BaseMessage: %v", err)
		return err
	}

	if err := SendMessageToGameClients(gameID, serializedBaseMessage, senderWebSocket); err != nil {
		log.Printf("Failed to send message to game clients: %v", err)
		return err
	}

	return nil
}
func SendStartGameMessage(gameID string, text string) error {
	log.Printf("Sending start game message for game_id %s", gameID)

	startMessage := &game.Start{
		GameId: []byte(gameID),
		Start:  true,
		Text:   []byte(text),
	}

	serializedStartMessage, err := SerializeToString(startMessage)
	if err != nil {
		log.Printf("Error serializing Start message: %v", err)
		return err
	}

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_START,
		Data:    serializedStartMessage,
	}

	serializedBaseMessage, err := SerializeToString(baseMessage)
	if err != nil {
		log.Printf("Error serializing BaseMessage: %v", err)
		return err
	}

	if err := SendStartGameMessageAndMark(gameID, serializedBaseMessage, nil); err != nil {
		log.Printf("Failed to send start game message: %v", err)
		return err
	}

	return nil
}

func SendStartGameMessageAndMark(gameID string, serializedMessage []byte, senderWebSocket *websocket.Conn) error {
	log.Printf("Sending message to game clients for game_id %s", gameID)

	var wg sync.WaitGroup

	mu.Lock()
	defer mu.Unlock()

	for clientConn, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				for _, user := range room.Users {
					log.Printf("Preparing to send message to client: session_id=%s, login=%s", user.SessionID, user.Login)
					user.setInGame(true)
				}

				wg.Add(1)
				go func(client *websocket.Conn) {
					defer wg.Done()
					if err := SendMessageToClient(client, serializedMessage); err != nil {
						log.Printf("Error sending message to client: %v", err)
					}
				}(clientConn)
			}
		}
	}

	wg.Wait()

	return nil
}

func SendDeleteMessage(gameID string) error {
	log.Printf("Sending delete message for game_id %s", gameID)
	var wg sync.WaitGroup

	for client, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				wg.Add(1)
				go func(client *websocket.Conn) {
					defer wg.Done()
					message := &game.DeleteCards{
						ClassId: game.ClassTypes_PROTO_TYPE_DELETE,
					}

					serializedMessage, err := SerializeToString(message)
					if err != nil {
						log.Printf("Failed to serialize DeleteCards message: %v", err)
						return
					}

					err = client.WriteMessage(websocket.BinaryMessage, serializedMessage)
					if err != nil {
						if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
							log.Printf("Connection closed while sending message: %v", err)
						} else {
							log.Printf("Error sending message to client: %v", err)
						}
						return
					}
					log.Println("Delete message sent to client")
				}(client)
			}
		}
	}

	wg.Wait()
	return nil
}

func SendUpdateMessage(login, sessionID, gameID string, websocket *websocket.Conn) error {
	log.Printf("Sending update message for game_id %s", gameID)

	message := &game.UpdateInfo{
		User: &game.User{
			Login:     []byte(login),
			SessionId: []byte(sessionID),
			GameId:    []byte(gameID),
		},
	}

	data, err := SerializeToString(message)
	if err != nil {
		return fmt.Errorf("failed to serialize UpdateInfo message: %v", err)
	}

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_UPDATE,
		Data:    data,
	}

	serializedMessage, err := SerializeToString(baseMessage)
	if err != nil {
		return fmt.Errorf("failed to serialize BaseMessage: %v", err)
	}

	return SendMessageToGameClients(gameID, serializedMessage, websocket)
}

func SendActionToGameClients(action *game.Action, senderWebSocket *websocket.Conn) error {
	log.Printf("Sending action to game clients for game_id %s", action.User.GameId)

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_ACTION,
	}

	data, err := SerializeToString(action)
	if err != nil {
		return fmt.Errorf("failed to serialize Action message: %v", err)
	}
	baseMessage.Data = data

	// Serialize the BaseMessage
	serializedMessage, err := SerializeToString(baseMessage)
	if err != nil {
		return fmt.Errorf("failed to serialize BaseMessage: %v", err)
	}

	return SendMessageToGameClients(string(action.User.GameId), serializedMessage, senderWebSocket)
}

func sendChatMessage(conn *websocket.Conn, gameID, login, message string) {
	chatMsg := &game.ChatMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_CHATMESSAGE,
		User: &game.User{
			Login:     []byte(login),
			SessionId: []byte(conn.RemoteAddr().String()),
			GameId:    []byte(gameID),
		},
		Message: []byte(message),
	}

	data, err := proto.Marshal(chatMsg)
	if err != nil {
		log.Printf("Error marshaling chat message: %v", err)
		return
	}

	baseMsg := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_CHATMESSAGE,
		Data:    data,
	}

	msgData, err := proto.Marshal(baseMsg)
	if err != nil {
		log.Printf("Error marshaling base message: %v", err)
		return
	}

	mu.Lock()
	for clientConn, rooms := range clients {
		for _, room := range rooms {
			if room.GameID == gameID {
				if err := clientConn.WriteMessage(websocket.BinaryMessage, msgData); err != nil {
					log.Printf("Error writing message to client: %v", err)
				}
			}
		}
	}
	mu.Unlock()
}

func sendUserDisconnectMessage(login, sessionID, gameID string) error {
	log.Printf("Sending user disconnect message for game_id %s and session_id %s", gameID, sessionID)

	userExitMessage := &game.UserInfo{
		User: &game.User{
			Login:     []byte(login),
			SessionId: []byte(sessionID),
			GameId:    []byte(gameID),
		},
		Connected: false,
	}

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_USERINFO,
	}

	serializedData, err := SerializeToString(userExitMessage)
	if err != nil {
		log.Printf("Error serializing UserInfo message: %v", err)
		return err
	}
	baseMessage.Data = serializedData

	serializedBaseMessage, err := SerializeToString(baseMessage)
	if err != nil {
		log.Printf("Error serializing BaseMessage: %v", err)
		return err
	}

	err = SendMessageToGameClients(gameID, serializedBaseMessage, nil)
	if err != nil {
		log.Printf("Error sending message to game clients: %v", err)
		return err
	}

	return nil
}

func sendUserStatus(sessionID, gameID string, senderWebSocket *websocket.Conn) error {
	log.Printf("Sending user status message for game_id %s and session_id %s", gameID, sessionID)
	userStatus := &game.Ready{
		ClassId: game.ClassTypes_PROTO_TYPE_STATUS,
		User: &game.User{
			SessionId: []byte(sessionID),
			GameId:    []byte(gameID),
		},
		Status: false,
	}

	SendStatusToGameClients(userStatus, senderWebSocket)
	return nil
}

func sendChosenID(chosenMsg *game.Choose, senderWebSocket *websocket.Conn) error {
	log.Printf("Sending chosen_id to game clients for game_id %s", string(chosenMsg.User.GameId))

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_CHOOSE,
	}

	data, err := SerializeToString(chosenMsg)
	if err != nil {
		log.Printf("Error serializing chosenMsg: %v", err)
		return err
	}
	baseMessage.Data = data

	serializedBaseMessage, err := SerializeToString(baseMessage)
	if err != nil {
		log.Printf("Error serializing BaseMessage: %v", err)
		return err
	}

	if err := SendMessageToGameClients(string(chosenMsg.User.GameId), serializedBaseMessage, senderWebSocket); err != nil {
		log.Printf("Failed to send message to game clients: %v", err)
		return err
	}

	return nil
}

func sendUpdateInfoToClient(gameInfo *game.GameInfo) error {
	log.Printf("Sending update info to client for destinationId %s", string(gameInfo.DestinationId))

	// Lock the mutex to safely access the shared clients map
	mu.Lock()
	defer mu.Unlock()

	for clientConn, rooms := range clients {
		for _, room := range rooms {
			for _, user := range room.Users {
				if string(user.SessionID) == string(gameInfo.DestinationId) {
					baseMessage := &game.BaseMessage{
						ClassId: game.ClassTypes_PROTO_TYPE_GAMEINFO,
					}

					// Serialize gameInfo to bytes
					data, err := SerializeToString(gameInfo)
					if err != nil {
						log.Printf("Error serializing gameInfo: %v", err)
						return err
					}
					baseMessage.Data = data

					serializedBaseMessage, err := SerializeToString(baseMessage)
					if err != nil {
						log.Printf("Error serializing BaseMessage: %v", err)
						return err
					}

					if err := SendMessageToClient(clientConn, serializedBaseMessage); err != nil {
						log.Printf("Error sending update to client %s: %v", gameInfo.DestinationId, err)
					} else {
						log.Printf("Sent update to client: %s", gameInfo.DestinationId)
					}
					return nil
				}
			}
		}
	}

	log.Printf("Client with destinationId %s not found", gameInfo.DestinationId)
	return nil
}
func SendMessageToGameClients(gameID string, serializedMessage []byte, senderWebSocket *websocket.Conn) error {
	log.Printf("Sending message to game clients for game_id %s", gameID)
	var wg sync.WaitGroup

	for client, clientRooms := range clients {
		for _, room := range clientRooms {
			if room.GameID == gameID {
				log.Printf("Preparing to send message to client: game_id=%s", room.GameID)
				wg.Add(1)
				go func(client *websocket.Conn, message []byte) {
					defer wg.Done()
					if err := SendMessageToClient(client, message); err != nil {
						log.Printf("Error sending message to client: %v", err)
					}
				}(client, serializedMessage)
			}
		}
	}

	wg.Wait()
	return nil
}

func SendUserInfoToGameClients(userInfo *game.UserInfo, senderWebSocket interface{}) error {
	log.Printf("Sending user info to game clients for game_id %s", userInfo.User.GameId)

	baseMessage := &game.BaseMessage{
		ClassId: game.ClassTypes_PROTO_TYPE_USERINFO,
	}

	userInfoData, err := SerializeToString(userInfo)
	if err != nil {
		log.Printf("Failed to serialize UserInfo: %v", err)
		return err
	}

	wsConn, ok := senderWebSocket.(*websocket.Conn)
	if !ok {
		return fmt.Errorf("failed to assert senderWebSocket to *websocket.Conn")
	}

	baseMessage.Data = userInfoData

	serializedMessage, err := SerializeToString(baseMessage)
	if err != nil {
		log.Printf("Failed to serialize BaseMessage: %v", err)
		return err
	}

	if err := SendMessageToGameClients(string(userInfo.User.GameId), serializedMessage, wsConn); err != nil {
		log.Printf("Failed to send message to game clients: %v", err)
		return err
	}

	return nil
}
