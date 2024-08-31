package main

import (
	"encoding/base64"
	"bufio"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var lock = &sync.Mutex{}

type Config struct {
	DatabaseURI       string
	UploadFolder      string
	UploadCards       string
	AllowedExtensions map[string]bool
}

var config = Config{
	DatabaseURI:       "users10.db",
	UploadFolder:      "uploads",
	UploadCards:       "cards",
	AllowedExtensions: map[string]bool{"png": true, "jpg": true, "jpeg": true},
}

type User struct {
	ID        uint   `gorm:"primaryKey"`
	Login     string `gorm:"unique;not null"`
	ImagePath string `gorm:"not null"`
	SessionID string `gorm:"unique;default:'0'"`
}

type Room struct {
	ID        uint   `gorm:"primaryKey"`
	GameID    string `gorm:"not null"`
	SessionID string `gorm:"not null"`
	Cards     string
}

type Situation struct {
	ID   uint   `gorm:"primaryKey"`
	Text string `gorm:"not null"`
}

type Card struct {
	ID      uint   `gorm:"primaryKey"`
	ImgPath string `gorm:"not null"`
}

type customDeck struct {
	ID      uint   `gorm:"primaryKey"`
	CardImg []byte `gorm:"not null"`
	DeckId  uint   `gorm:"not null"`
	GameId  string `gorm:"not null"`
}

func main() {
	db, err := gorm.Open(sqlite.Open(config.DatabaseURI), &gorm.Config{})
	if err != nil {
		panic("failed to connect to database")
	}

	db.AutoMigrate(&User{}, &Room{}, &Situation{}, &Card{}, &customDeck{})

	populateSituations(db)
	testCards(db)

	r := gin.Default()
	r.POST("/register", func(c *gin.Context) { register(db, c) })
	r.POST("/user-info", func(c *gin.Context) { reload(db, c) })
	r.GET("/text", func(c *gin.Context) { getText(db, c) })
	r.GET("/cards", func(c *gin.Context) { getCard(db, c) })
	r.POST("/exit", func(c *gin.Context) { exit(db, c) })
	r.POST("/disconnect", func(c *gin.Context) { disconnect(db, c) })
	r.POST("/connect", func(c *gin.Context) { connect(db, c) })
	r.GET("/room-stats", func(c *gin.Context) { roomStats(db, c) })
	r.POST("/host", func(c *gin.Context) { host(db, c) })
	r.POST("/createCustomDeck", func(c *gin.Context) { CreateCustomDeck(db, c) })
	r.POST("/generateRandomCustomDeck", func(c *gin.Context) { GenerateRandomCustomDeck(db, c) })

	os.MkdirAll(config.UploadFolder, os.ModePerm)
	r.Run(":8080")
}

func CreateCustomDeck(db *gorm.DB, c *gin.Context) {
	var request struct {
		CardImgs [][]byte `json:"cardImgs"`
		GameId   string   `json:"gameId"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var maxDeckId struct {
		MaxDeckId uint
	}
	if err := db.Model(&customDeck{}).Select("MAX(deck_id) as max_deck_id").Scan(&maxDeckId).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get max deck ID"})
		return
	}

	newDeckId := maxDeckId.MaxDeckId + 1

	for _, cardImg := range request.CardImgs {
		if err := db.Create(&customDeck{
			CardImg: cardImg,
			DeckId:  newDeckId,
			GameId:  request.GameId,
		}).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create custom deck"})
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{"deckId": newDeckId})
}

func GenerateRandomCustomDeck(db *gorm.DB, c *gin.Context) {
	var request struct {
		DeckId    uint   `json:"deckId"`
		GameId    string `json:"gameId"`
		SessionID string `json:"sessionId"`
	}

	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var cards []customDeck
	if err := db.Where("deck_id = ?", request.DeckId).Find(&cards).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to get cards"})
		return
	}

	if len(cards) < 6 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Not enough cards in the deck"})
		return
	}

	rand.Seed(time.Now().UnixNano())
	rand.Shuffle(len(cards), func(i, j int) { cards[i], cards[j] = cards[j], cards[i] })

	selectedCards := cards[:6]

	var cardImgs [][]byte
	for _, card := range selectedCards {
		cardImgs = append(cardImgs, card.CardImg)
	}

	c.JSON(http.StatusOK, gin.H{
		"cardImgs":  cardImgs,
		"gameId":    request.GameId,
		"sessionId": request.SessionID,
	})
}

func register(db *gorm.DB, c *gin.Context) {
	login := c.PostForm("login")
	file, err := c.FormFile("image")

	if login == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Login can't be empty"})
		return
	}

	var user User
	if err := db.Where("login = ?", login).First(&user).Error; err == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Login exists"})
		return
	}

	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Failed to get file"})
		return
	}

	if file != nil && allowedFile(file.Filename) {
		filename := secureFilename(file.Filename)
		imagePath := filepath.Join(config.UploadFolder, filename)

		if err := c.SaveUploadedFile(file, imagePath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to save file"})
			return
		}

		sessionID := uuid.New().String()
		user := User{Login: login, ImagePath: imagePath, SessionID: sessionID}
		// Create the new user
		if err := db.Create(&user).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create user"})
			return
		}

		c.JSON(http.StatusCreated, gin.H{"session_id": sessionID})
	} else {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid file"})
	}
}

func reload(db *gorm.DB, c *gin.Context) {
	sessionID := c.PostForm("session_id")
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing session_id"})
		return
	}

	var user User
	if err := db.Where("session_id = ?", sessionID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	imageBytes, err := os.ReadFile(user.ImagePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read image file"})
		return
	}

	b64image := base64.StdEncoding.EncodeToString(imageBytes)
	c.JSON(http.StatusOK, gin.H{
		"session_id": user.SessionID,
		"login":      user.Login,
		"image_data": b64image,
	})
}

func getText(db *gorm.DB, c *gin.Context) {
	var situation Situation
	if err := db.Order("RANDOM()").First(&situation).Error; err != nil {
		log.Printf("Error fetching random situation: %v", err)
		c.JSON(http.StatusNotFound, gin.H{"error": "No situations available"})
		return
	}
	log.Printf("Fetched situation: %s", situation.Text)
	c.JSON(http.StatusOK, gin.H{"text": situation.Text})
}

func getCard(db *gorm.DB, c *gin.Context) {
	var card Card
	if err := db.Order("RANDOM()").First(&card).Error; err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "No cards image available"})
		return
	}

	if _, err := os.Stat(card.ImgPath); os.IsNotExist(err) {
		c.JSON(http.StatusNotFound, gin.H{"error": "File not found"})
		return
	}

	lock.Lock()
	imageBytes, err := os.ReadFile(card.ImgPath)
	lock.Unlock()

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read image file"})
		return
	}

	b64image := base64.StdEncoding.EncodeToString(imageBytes)
	c.JSON(http.StatusOK, gin.H{"card_img": b64image})
}

func exit(db *gorm.DB, c *gin.Context) {
	var json struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&json); err != nil || json.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing session_id"})
		return
	}

	var user User
	if err := db.Where("session_id = ?", json.SessionID).First(&user).Error; err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid session_id"})
		return
	}

	var room Room
	if err := db.Where("session_id = ?", json.SessionID).First(&room).Error; err == nil {
		gameID := room.GameID
		db.Delete(&room)

		var count int64
		db.Model(&Room{}).Where("game_id = ?", gameID).Count(&count)
		if count == 0 {
			db.Where("game_id = ?", gameID).Delete(&Room{})
		}
	}

	os.Remove(user.ImagePath)
	db.Delete(&user)

	c.JSON(http.StatusNoContent, gin.H{"message": "User successfully exited and data deleted"})
}

func disconnect(db *gorm.DB, c *gin.Context) {
	var json struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&json); err != nil || json.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Missing session_id"})
		return
	}

	var room Room
	if err := db.Where("session_id = ?", json.SessionID).First(&room).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not in any game"})
		return
	}

	gameID := room.GameID
	db.Delete(&room)

	var count int64
	db.Model(&Room{}).Where("game_id = ?", gameID).Count(&count)
	if count == 0 {
		db.Where("game_id = ?", gameID).Delete(&Room{})
	}

	c.JSON(http.StatusOK, gin.H{"message": "Successfully disconnected from the game"})
}

func connect(db *gorm.DB, c *gin.Context) {
	var json struct {
		GameID    string `json:"game_id"`
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&json); err != nil || json.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session ID is required"})
		return
	}
	if json.GameID == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Game ID is required"})
		return
	}

	var user User
	if err := db.Where("session_id = ?", json.SessionID).First(&user).Error; err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid session_id"})
		return
	}

	var rooms []Room
	db.Where("game_id = ?", json.GameID).Find(&rooms)
	if len(rooms) >= 4 {
		c.JSON(http.StatusForbidden, gin.H{"error": "Lobby is full"})
		return
	}

	for _, room := range rooms {
		if room.SessionID == json.SessionID {
			c.JSON(http.StatusConflict, gin.H{"error": "Session already connected to this game"})
			return
		}
	}

	newRoom := Room{GameID: json.GameID, SessionID: json.SessionID}
	db.Create(&newRoom)

	var usersData []map[string]interface{}
	for _, room := range rooms {
		var user User
		if err := db.Where("session_id = ?", room.SessionID).First(&user).Error; err == nil {
			imageBytes, err := os.ReadFile(user.ImagePath)
			if err != nil {
				continue
			}
			b64image := base64.StdEncoding.EncodeToString(imageBytes)
			usersData = append(usersData, map[string]interface{}{
				"session_id": user.SessionID,
				"login":      user.Login,
				"image_data": b64image,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"game_id":  json.GameID,
		"sessions": usersData,
	})
}

func roomStats(db *gorm.DB, c *gin.Context) {
	var result []struct {
		GameID      string
		PlayerCount int
	}
	db.Raw(`SELECT r.game_id, COUNT(u.id) AS player_count
		FROM rooms r
		LEFT JOIN users u ON r.session_id = u.session_id
		GROUP BY r.game_id`).Scan(&result)

	var roomStats []map[string]interface{}
	for _, res := range result {
		roomStats = append(roomStats, map[string]interface{}{
			"game_id":      res.GameID,
			"player_count": res.PlayerCount,
		})
	}

	c.JSON(http.StatusOK, roomStats)
}

func host(db *gorm.DB, c *gin.Context) {
	var json struct {
		SessionID string `json:"session_id"`
	}
	if err := c.ShouldBindJSON(&json); err != nil || json.SessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session ID is required"})
		return
	}

	var user User
	if err := db.Where("session_id = ?", json.SessionID).First(&user).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "User not found"})
		return
	}

	gameID := generateGameID()
	newRoom := Room{GameID: gameID, SessionID: json.SessionID, Cards: "0"}
	db.Create(&newRoom)

	c.JSON(http.StatusCreated, gin.H{"message": "Room created successfully", "game_id": gameID})
}

func generateGameID() string {
	rand.Seed(time.Now().UnixNano())
	letters := []rune("ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	b := make([]rune, 6)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func allowedFile(filename string) bool {
	allowedExtensions := map[string]bool{
		"jpg":  true,
		"jpeg": true,
		"png":  true,
	}
	ext := filepath.Ext(filename)
	return allowedExtensions[strings.TrimPrefix(ext, ".")]
}

func secureFilename(filename string) string {
	return filepath.Base(filename)
}

func getSituationsFromFile(filename string) []string {
	var situations []string

	file, err := os.Open(filename)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)

	for scanner.Scan() {
		situations = append(situations, scanner.Text())
	}

	if err := scanner.Err(); err != nil {
		log.Fatal(err)
	}

	return situations
}

func populateSituations(db *gorm.DB) {
	situations := getSituationsFromFile("situations.txt")

	for _, situation := range situations {
		var s Situation
		if err := db.Where("text = ?", situation).First(&s).Error; err != nil {
			db.Create(&Situation{Text: situation})
		}
	}
}

func testCards(db *gorm.DB) {
	cards := []string{
		"cards\\test.png",
		"cards\\test2.png",
	}
	for _, card := range cards {
		var c Card
		if err := db.Where("img_path = ?", card).First(&c).Error; err != nil {
			db.Create(&Card{ImgPath: card})
		}
	}
}
