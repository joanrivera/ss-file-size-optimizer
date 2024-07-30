package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"golang.org/x/image/draw"
)

const command = "pngquant"

var cmdArgs = map[string]string{"imageQuality": "16"}

//go:embed index.html
var indexHtml embed.FS

func main() {
	// Check if the processing command is available
	if err := checkCommandAvailable(command); err != nil {
		fmt.Println("Processing command not available: " + command)
		os.Exit(1)
	}

	// Read port from command line
	port := flag.String("port", "60031", "Port to run the server on")
	flag.Parse()

	address := fmt.Sprintf(":%s", *port)

	// Listen on the provided port
	listener, err := net.Listen("tcp", address)
	if err != nil {
		fmt.Println("Error creating listener:", err)
		return
	}

	http.HandleFunc("/upload", uploadHandler)
	http.HandleFunc("/", indexPageHandler)

	// Open the web browser to the HTML location
	msg := fmt.Sprintf("http://localhost%s", address)
	fmt.Println(msg)
	openBrowser(msg)

	// Start the server with the listener
	http.Serve(listener, nil)
}

func uploadHandler(w http.ResponseWriter, r *http.Request) {
	// Set CORS headers to allow all origins
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

	// Handle preflight request
	if r.Method == http.MethodOptions {
		return
	}

	if r.Method != http.MethodPost {
		m := "Only POST method is supported"
		http.Error(w, m, http.StatusMethodNotAllowed)
		fmt.Println(m)
		return
	}

	// Get the image quality from the form data
	qualityStr := r.FormValue("imageQuality")
	quality, err := strconv.Atoi(qualityStr)
	if err != nil || quality < 0 || quality > 100 {
		http.Error(w, "Invalid quality value. It should be between 0 and 100", http.StatusBadRequest)
		return
	}
	cmdArgs["imageQuality"] = qualityStr

	file, header, err := r.FormFile("image")
	if err != nil {
		m := "Error reading the file"
		http.Error(w, m, http.StatusBadRequest)
		fmt.Println(m)
		return
	}
	defer file.Close()

	// Generate a unique file name
	fileName := fmt.Sprintf("%d_%s", time.Now().UnixNano(), header.Filename)
	filePath := filepath.Join("uploads", fileName)

	// Create the uploads directory if it doesn't exist
	if err := os.MkdirAll("uploads", os.ModePerm); err != nil {
		m := "Error creating directory"
		http.Error(w, m, http.StatusInternalServerError)
		fmt.Println(m)
		return
	}

	// Save the file to disk
	out, err := os.Create(filePath)
	if err != nil {
		m := "Error saving the file"
		http.Error(w, m, http.StatusInternalServerError)
		fmt.Println(m)
		return
	}
	defer out.Close()

	// Copy the uploaded file to the output file
	if _, err := io.Copy(out, file); err != nil {
		m := "Error saving the file"
		http.Error(w, m, http.StatusInternalServerError)
		fmt.Println(m)
		return
	}

	maxWidth, _ := strconv.Atoi(r.FormValue("maxWidth"))
	maxHeight, _ := strconv.Atoi(r.FormValue("maxHeight"))

	// Resize the image
	if err := resizeImage(filePath, filePath, maxWidth, maxHeight); err != nil {
		m := "Error resizing the image"
		http.Error(w, m, http.StatusInternalServerError)
		fmt.Println(m)
		return
	}

	// Process the file using an OS command
	if err := processFile(filePath); err != nil {
		m := "Error processing the file"
		http.Error(w, m, http.StatusInternalServerError)
		fmt.Println(m)
		return
	}

	// Read the processed file into a buffer
	processedFile, err := os.Open(filePath + "-optimized.png")
	if err != nil {
		m := "Error opening the processed file"
		http.Error(w, m, http.StatusInternalServerError)
		fmt.Println(m)
		return
	}
	defer processedFile.Close()

	buf := make([]byte, 0, 512)
	tmpBuf := make([]byte, 512)
	for {
		n, err := processedFile.Read(tmpBuf)
		if err != nil && err != io.EOF {
			m := "Error reading the processed file"
			http.Error(w, m, http.StatusInternalServerError)
			fmt.Println(m)
			return
		}
		if n == 0 {
			break
		}
		buf = append(buf, tmpBuf[:n]...)
	}

	// Encode the buffer to base64
	base64Str := base64.StdEncoding.EncodeToString(buf)

	// Return the base64 string as JSON
	w.Header().Set("Content-Type", "application/json")
	data := map[string]string{
		"base64":    base64Str,
		"file_path": html.EscapeString(filePath),
		"file_size": fmt.Sprintf("%d", len(buf)),
	}
	jsonData, _ := json.Marshal(data)
	fmt.Fprintf(w, string(jsonData))
}

func indexPageHandler(w http.ResponseWriter, r *http.Request) {
	// Read the embedded HTML file
	data, err := indexHtml.ReadFile("index.html")
	if err != nil {
		http.Error(w, "Could not read embedded file", http.StatusInternalServerError)
		return
	}

	// Write the HTML content to the response
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write(data)
}

func checkCommandAvailable(command string) error {
	_, err := exec.LookPath(command)
	return err
}

func processFile(filePath string) error {
	cmd := exec.Command(
		command,
		"--quality="+cmdArgs["imageQuality"],
		"--speed=1",
		"--force",
		filePath,
		"--output",
		filePath+"-optimized.png",
	)
	err := cmd.Run()
	if err != nil {
		fmt.Println(err)
		fmt.Println(cmd.Stdout)
	}
	return err
}

// resizeImage resizes an image to ensure it does not exceed maxWidth and maxHeight while maintaining the aspect ratio.
func resizeImage(inputPath, outputPath string, maxWidth, maxHeight int) error {
	// Open the input image file
	file, err := os.Open(inputPath)
	if err != nil {
		return fmt.Errorf("failed to open input image: %v", err)
	}
	defer file.Close()

	// Decode the image
	img, format, err := image.Decode(file)
	if err != nil {
		return fmt.Errorf("failed to decode image: %v", err)
	}

	// Calculate the new dimensions while maintaining the aspect ratio
	originalWidth := img.Bounds().Dx()
	originalHeight := img.Bounds().Dy()

	width := originalWidth
	height := originalHeight

	if width > maxWidth {
		width = maxWidth
		height = int(float64(originalHeight) * float64(maxWidth) / float64(originalWidth))
	}
	if height > maxHeight {
		height = maxHeight
		width = int(float64(originalWidth) * float64(maxHeight) / float64(originalHeight))
	}

	// Create a new image with the new dimensions
	newImg := image.NewRGBA(image.Rect(0, 0, width, height))

	// Resize the image
	draw.CatmullRom.Scale(newImg, newImg.Bounds(), img, img.Bounds(), draw.Over, nil)

	// Create the output image file
	outFile, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output image: %v", err)
	}
	defer outFile.Close()

	// Encode and save the resized image
	switch format {
	case "jpeg":
		err = jpeg.Encode(outFile, newImg, nil)
	case "png":
		err = png.Encode(outFile, newImg)
	default:
		return fmt.Errorf("unsupported image format: %s", format)
	}

	if err != nil {
		return fmt.Errorf("failed to encode image: %v", err)
	}

	return nil
}

func openBrowser(url string) {
	var err error

	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}

	if err != nil {
		fmt.Printf("Failed to open browser: %v\n", err)
	}
}
