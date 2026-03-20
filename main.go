package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path"

	"go-api-s3/docs"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/joho/godotenv"
	httpSwagger "github.com/swaggo/http-swagger"
)

var s3Client *s3.Client

var bucketName string

func enableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
	(*w).Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS, POST")
	(*w).Header().Set("Access-Control-Allow-Headers", "Content-Type")
}

func initAWS() {
	err := godotenv.Load()
	if err != nil {
		log.Println("Environment file not found. Using system environment variables.")
	}

	bucketName = os.Getenv("AWS_BUCKET_NAME")
	if bucketName == "" {
		log.Fatalf("AWS_BUCKET_NAME environment variable is not set")
	}

	cfg, err := config.LoadDefaultConfig(context.TODO())
	if err != nil {
		log.Fatalf("Failed to load AWS configuration: %v", err)
	}

	customEndpoint := os.Getenv("AWS_ENDPOINT_URL")

	if customEndpoint != "" {
		s3Client = s3.NewFromConfig(cfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(customEndpoint)
			o.UsePathStyle = true
		})
	} else {
		s3Client = s3.NewFromConfig(cfg)
	}
}

func testS3Connection() {
	output, err := s3Client.ListObjectsV2(context.TODO(), &s3.ListObjectsV2Input{
		Bucket:  aws.String(bucketName),
		MaxKeys: aws.Int32(1),
	})

	if err != nil {
		log.Fatalf("CRITICAL ERROR: Failed to connect to S3 bucket. Details: %v", err)
	}

	if len(output.Contents) == 0 {
		log.Fatalf("CRITICAL ERROR: Connected to S3, but the bucket is completely empty.")
	}

	log.Println("S3 connection test passed successfully. Files detected in bucket.")
}

// statusHandler
// @Summary Health check
// @Description Returns service health and basic status information.
// @Tags health
// @Produce json
// @Success 200 {object} map[string]string
// @Router / [get]
func statusHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "OK",
		"message": "Service is running optimally.",
	})
}

// sendHandler
// @Summary Upload file to S3
// @Description Uploads a single file to the configured S3 bucket under the provided folder.
// @Tags files
// @Accept mpfd
// @Produce json
// @Param folder formData string true "Target folder inside the bucket"
// @Param file formData file true "File to upload"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "Bad Request"
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /send [post]
func sendHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		http.Error(w, "Failed to parse multipart form.", http.StatusBadRequest)
		return
	}

	folder := r.FormValue("folder")
	if folder == "" {
		http.Error(w, "Folder parameter is required.", http.StatusBadRequest)
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "File is required.", http.StatusBadRequest)
		return
	}
	defer file.Close()

	key := path.Join(folder, handler.Filename)

	_, err = s3Client.PutObject(context.TODO(), &s3.PutObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
		Body:   file,
	})

	if err != nil {
		http.Error(w, "Internal server error during upload.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "File uploaded successfully.",
		"path":    key,
	})
}

// getHandler
// @Summary Download file from S3
// @Description Downloads a file from the configured S3 bucket using folder and file query parameters.
// @Tags files
// @Produce octet-stream
// @Param folder query string true "Folder inside the bucket"
// @Param file query string true "File name to download"
// @Success 200 {file} binary
// @Failure 400 {string} string "Bad Request"
// @Failure 404 {string} string "Not Found"
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /get [get]

func getHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	fileName := r.URL.Query().Get("file")

	if folder == "" || fileName == "" {
		http.Error(w, "Folder and file parameters are required.", http.StatusBadRequest)
		return
	}

	key := path.Join(folder, fileName)

	result, err := s3Client.GetObject(context.TODO(), &s3.GetObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		http.Error(w, "Requested file not found.", http.StatusNotFound)
		return
	}
	defer result.Body.Close()

	w.Header().Set("Content-Disposition", "inline; filename="+fileName)
	w.Header().Set("Content-Type", "application/pdf")

	_, err = io.Copy(w, result.Body)
	if err != nil {
		http.Error(w, "Internal server error during file download.", http.StatusInternalServerError)
		return
	}
}

// deleteHandler
// @Summary Delete file from S3
// @Description Deletes a file from the configured S3 bucket using folder and file query parameters.
// @Tags files
// @Produce json
// @Param folder query string true "Folder inside the bucket"
// @Param file query string true "File name to delete"
// @Success 200 {object} map[string]string
// @Failure 400 {string} string "Bad Request"
// @Failure 404 {string} string "Not Found"
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /delete [delete]
func deleteHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	fileName := r.URL.Query().Get("file")

	if folder == "" || fileName == "" {
		http.Error(w, "Folder and file parameters are required.", http.StatusBadRequest)
		return
	}

	key := path.Join(folder, fileName)

	_, err := s3Client.DeleteObject(context.TODO(), &s3.DeleteObjectInput{
		Bucket: aws.String(bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		http.Error(w, "Internal server error during file deletion.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": "File deleted successfully.",
		"path":    key,
	})
}

func main() {
	initAWS()
	testS3Connection()

	if host := os.Getenv("SWAGGER_HOST"); host != "" {
		docs.SwaggerInfo.Host = host
	}

	http.HandleFunc("/", statusHandler)
	http.HandleFunc("/send", sendHandler)
	http.HandleFunc("/get", getHandler)
	http.HandleFunc("/delete", deleteHandler)
	http.HandleFunc("/docs/", httpSwagger.WrapHandler)

	log.Println("Server is listening on port 8080.")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
