package main

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

var (
	directoriesWithImages []string
	directoriesMutex      sync.RWMutex
	sftpClient            *sftp.Client
	clientMutex           sync.RWMutex
)

type ImageInfo struct {
	Path         string    `json:"path"`
	CreationDate time.Time `json:"creation_date"`
}

func isImageFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	imageExts := []string{".jpg", ".jpeg", ".png", ".gif", ".bmp", ".webp", ".tiff", ".tif", ".svg"}
	for _, imgExt := range imageExts {
		if ext == imgExt {
			return true
		}
	}
	return false
}

func getContentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".bmp":
		return "image/bmp"
	case ".webp":
		return "image/webp"
	case ".tiff", ".tif":
		return "image/tiff"
	case ".svg":
		return "image/svg+xml"
	default:
		return "image/jpeg"
	}
}

func getRandomImage(c *gin.Context) {
	directoriesMutex.RLock()
	if len(directoriesWithImages) == 0 {
		directoriesMutex.RUnlock()
		c.JSON(http.StatusNotFound, gin.H{"error": "No directories with images found"})
		return
	}
	directoriesMutex.RUnlock()

	directoriesMutex.RLock()
	randomDir := directoriesWithImages[rand.Intn(len(directoriesWithImages))]
	directoriesMutex.RUnlock()

	clientMutex.RLock()
	client := sftpClient
	clientMutex.RUnlock()

	entries, err := client.ReadDir(randomDir)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read directory: " + err.Error()})
		return
	}

	var images []ImageInfo
	for _, entry := range entries {
		if !entry.IsDir() && isImageFile(entry.Name()) {
			fullPath := filepath.Join(randomDir, entry.Name())
			images = append(images, ImageInfo{
				Path:         fullPath,
				CreationDate: entry.ModTime(),
			})
		}
	}

	if len(images) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "No images found in selected directory"})
		return
	}

	randomImage := images[rand.Intn(len(images))]

	file, err := client.Open(randomImage.Path)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to open image file: " + err.Error()})
		return
	}
	defer file.Close()

	imageData, err := io.ReadAll(file)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to read image file: " + err.Error()})
		return
	}

	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")

	contentType := getContentType(randomImage.Path)
	c.Header("Content-Type", contentType)
	c.Header("X-Creation-Date", randomImage.CreationDate.Format(time.RFC3339))
	c.Data(http.StatusOK, contentType, imageData)
}

func handleOptions(c *gin.Context) {
	c.Header("Access-Control-Allow-Origin", "*")
	c.Header("Access-Control-Allow-Methods", "GET, OPTIONS")
	c.Header("Access-Control-Allow-Headers", "Content-Type")
	c.Status(http.StatusOK)
}

func listFoldersRecursively(client *sftp.Client, rootPath string, indent string) error {
	entries, err := client.ReadDir(rootPath)
	if err != nil {
		return err
	}

	hasImages := false
	for _, entry := range entries {
		if !entry.IsDir() && isImageFile(entry.Name()) {
			hasImages = true
			break
		}
	}

	if hasImages {
		directoriesMutex.Lock()
		directoriesWithImages = append(directoriesWithImages, rootPath)
		directoriesMutex.Unlock()
		fmt.Printf("%s%s/ (contains images)\n", indent, filepath.Base(rootPath))
	} else {
		fmt.Printf("%s%s/\n", indent, filepath.Base(rootPath))
	}

	for _, entry := range entries {
		if entry.IsDir() {
			fullPath := filepath.Join(rootPath, entry.Name())
			err := listFoldersRecursively(client, fullPath, indent+"  ")
			if err != nil {
				fmt.Printf("Error reading %s: %v\n", fullPath, err)
			}
		}
	}
	return nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Warning: Error loading .env file:", err)
		fmt.Println("Continuing with system environment variables...")
	}

	sshUser := getEnv("SSH_USER", "")
	sshPassword := getEnv("SSH_PASSWORD", "")
	sshHost := getEnv("SSH_HOST", "")
	sshPort := getEnv("SSH_PORT", "22")
	serverHost := getEnv("SERVER_HOST", "localhost")
	serverPort := getEnv("SERVER_PORT", "3141")

	if sshUser == "" || sshPassword == "" || sshHost == "" {
		panic("Missing required environment variables: SSH_USER, SSH_PASSWORD, and SSH_HOST must be set")
	}

	config := &ssh.ClientConfig{
		User: sshUser,
		Auth: []ssh.AuthMethod{
			ssh.Password(sshPassword),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}

	address := fmt.Sprintf("%s:%s", sshHost, sshPort)
	conn, err := ssh.Dial("tcp", address, config)
	if err != nil {
		panic("Failed to connect to NAS: " + err.Error())
	}
	defer conn.Close()

	client, err := sftp.NewClient(conn)
	if err != nil {
		panic("Failed to create SFTP client: " + err.Error())
	}
	defer client.Close()

	clientMutex.Lock()
	sftpClient = client
	clientMutex.Unlock()

	rand.Seed(time.Now().UnixNano())

	err = listFoldersRecursively(client, "/", "")
	if err != nil {
		fmt.Printf("Error listing folders: %v\n", err)
	}

	directoriesMutex.RLock()
	fmt.Printf("Found %d directories with images\n", len(directoriesWithImages))
	directoriesMutex.RUnlock()

	router := gin.Default()

	router.GET("/getRandomImage", getRandomImage)
	router.OPTIONS("/getRandomImage", handleOptions)

	serverAddress := fmt.Sprintf("%s:%s", serverHost, serverPort)
	fmt.Printf("Server starting on %s\n", serverAddress)
	router.Run(serverAddress)
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
