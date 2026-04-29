package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"

	"encoding/csv"
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

	ext := path.Ext(fileName)
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		if result.ContentType != nil && *result.ContentType != "" {
			contentType = *result.ContentType
		} else {
			contentType = "application/octet-stream"
		}
	}

	w.Header().Set("Content-Disposition", "inline; filename=\""+fileName+"\"")
	w.Header().Set("Content-Type", contentType)

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

// listHandler
// @Summary List files in S3 folder
// @Description Lists all files in a specified folder within the configured S3 bucket and returns their download links.
// @Tags files
// @Produce json
// @Param folder query string true "Folder inside the bucket"
// @Success 200 {array} string
// @Failure 400 {string} string "Bad Request"
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /list [get]
func listHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		http.Error(w, "Folder parameter is required.", http.StatusBadRequest)
		return
	}

	prefix := folder
	if prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}

	log.Printf("Memulai proses penarikan data S3 untuk folder: %s", folder)

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket: aws.String(bucketName),
		Prefix: aws.String(prefix),
	})

	links := make([]string, 0, 150000)
	pageCount := 0
	totalFetched := 0

	for paginator.HasMorePages() {
		pageCount++
		log.Printf("Memproses halaman %d...", pageCount)

		page, err := paginator.NextPage(context.TODO())
		if err != nil {
			log.Printf("ERROR: Gagal menarik data pada halaman %d: %v", pageCount, err)
			http.Error(w, "Internal server error while listing files.", http.StatusInternalServerError)
			return
		}

		currentBatchCount := len(page.Contents)
		totalFetched += currentBatchCount

		log.Printf("Halaman %d berhasil ditarik: +%d file. Total file sementara: %d", pageCount, currentBatchCount, totalFetched)

		for _, obj := range page.Contents {
			if *obj.Key == prefix || strings.HasSuffix(*obj.Key, "/") {
				continue
			}

			dir, file := path.Split(*obj.Key)
			dir = strings.TrimSuffix(dir, "/")

			link := "https://s3.pnj-digit.site/get?folder=" + dir + "&file=" + file
			links = append(links, link)
		}
	}

	log.Printf("Proses selesai! Total %d halaman ditarik. Total link dibuat: %d. Mengirimkan response...", pageCount, len(links))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(links)
}

// listFolderHandler
// @Summary List folders in S3
// @Description Lists all subfolders within a specified folder in the configured S3 bucket.
// @Tags folders
// @Produce json
// @Param folder query string true "Parent folder inside the bucket"
// @Success 200 {array} string
// @Failure 400 {string} string "Bad Request"
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /list-folders [get]
func listFolderHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	folder := r.URL.Query().Get("folder")
	if folder == "" {
		http.Error(w, "Folder parameter is required.", http.StatusBadRequest)
		return
	}

	prefix := folder
	if prefix[len(prefix)-1] != '/' {
		prefix += "/"
	}

	log.Printf("Memulai proses penarikan daftar folder NPSN untuk penyedia: %s", folder)

	paginator := s3.NewListObjectsV2Paginator(s3Client, &s3.ListObjectsV2Input{
		Bucket:    aws.String(bucketName),
		Prefix:    aws.String(prefix),
		Delimiter: aws.String("/"),
	})

	var folders []string
	pageCount := 0

	for paginator.HasMorePages() {
		pageCount++
		log.Printf("Memproses halaman %d untuk daftar folder...", pageCount)

		page, err := paginator.NextPage(context.TODO())
		if err != nil {
			log.Printf("ERROR: Gagal menarik data folder pada halaman %d: %v", pageCount, err)
			http.Error(w, "Internal server error while listing folders.", http.StatusInternalServerError)
			return
		}

		for _, commonPrefix := range page.CommonPrefixes {
			folderPath := *commonPrefix.Prefix
			folderName := strings.TrimPrefix(folderPath, prefix)
			folderName = strings.TrimSuffix(folderName, "/")

			if folderName != "" {
				folders = append(folders, folderName)
			}
		}
	}

	if folders == nil {
		folders = []string{}
	}

	log.Printf("Proses list folder selesai! Total folder NPSN ditemukan: %d.", len(folders))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(folders)
}

// getDacHandler
// @Summary Get DAC data
// @Description Retrieves and processes DAC data from a local JSON file, grouping links by NPSN.
// @Tags Migrate Digit 2025
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /dac [get]
func getDacHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	file, err := os.Open("public/dac.json")
	if err != nil {
		http.Error(w, "Failed to open DAC data file.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var rawLinks []string
	err = json.NewDecoder(file).Decode(&rawLinks)
	if err != nil {
		http.Error(w, "Failed to decode DAC data.", http.StatusInternalServerError)
		return
	}

	groupedData := make(map[string][]string)

	for _, link := range rawLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		folderParam := u.Query().Get("folder")
		slashIndex := strings.Index(folderParam, "/")

		if slashIndex != -1 && len(folderParam) >= slashIndex+9 {
			npsn := folderParam[slashIndex+1 : slashIndex+9]
			groupedData[npsn] = append(groupedData[npsn], link)
		}
	}

	type NpsnData struct {
		Npsn string   `json:"npsn"`
		Path []string `json:"path"`
	}

	var resultData []NpsnData
	for npsn, paths := range groupedData {
		resultData = append(resultData, NpsnData{
			Npsn: npsn,
			Path: paths,
		})
	}

	response := map[string]interface{}{
		"status": "success",
		"data":   resultData,
	}

	json.NewEncoder(w).Encode(response)
}

// getDacCsvHandler
// @Summary Get DAC data as CSV
// @Description Retrieves DAC data from a local JSON file and returns it as a downloadable CSV file.
// @Tags Migrate Digit 2025
// @Produce text/csv
// @Success 200 {file} binary
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /dac.csv [get]
func getDacCsvHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	file, err := os.Open("public/dac.json")
	if err != nil {
		http.Error(w, "Failed to open DAC data file.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var rawLinks []string
	err = json.NewDecoder(file).Decode(&rawLinks)
	if err != nil {
		http.Error(w, "Failed to decode DAC data.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=dac_data.csv")
	w.Header().Set("Content-Type", "text/csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"npsn", "path"})

	for _, link := range rawLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		folderParam := u.Query().Get("folder")
		slashIndex := strings.Index(folderParam, "/")

		if slashIndex != -1 && len(folderParam) >= slashIndex+9 {
			npsn := folderParam[slashIndex+1 : slashIndex+9]
			writer.Write([]string{npsn, link})
		}
	}
}

// getZyrexHandler
// @Summary Get Zyrex data
// @Description Retrieves and processes Zyrex data from a local JSON file, grouping links by NPSN.
// @Tags Migrate Digit 2025
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /zyrex [get]
func getZyrexHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	file, err := os.Open("public/zyrex.json")
	if err != nil {
		http.Error(w, "Failed to open Zyrex data file.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var rawLinks []string
	err = json.NewDecoder(file).Decode(&rawLinks)
	if err != nil {
		http.Error(w, "Failed to decode Zyrex data.", http.StatusInternalServerError)
		return
	}

	groupedData := make(map[string][]string)

	for _, link := range rawLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		folderParam := u.Query().Get("folder")
		slashIndex := strings.Index(folderParam, "/")

		if slashIndex != -1 && len(folderParam) >= slashIndex+9 {
			npsn := folderParam[slashIndex+1 : slashIndex+9]
			groupedData[npsn] = append(groupedData[npsn], link)
		}
	}

	type NpsnData struct {
		Npsn string   `json:"npsn"`
		Path []string `json:"path"`
	}

	var resultData []NpsnData
	for npsn, paths := range groupedData {
		resultData = append(resultData, NpsnData{
			Npsn: npsn,
			Path: paths,
		})
	}

	response := map[string]interface{}{
		"status": "success",
		"data":   resultData,
	}

	json.NewEncoder(w).Encode(response)
}

// getZyrexCsvHandler
// @Summary Get Zyrex data as CSV
// @Description Retrieves Zyrex data from a local JSON file and returns it as a downloadable CSV file.
// @Tags Migrate Digit 2025
// @Produce text/csv
// @Success 200 {file} binary
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /zyrex.csv [get]
func getZyrexCsvHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	file, err := os.Open("public/zyrex.json")
	if err != nil {
		http.Error(w, "Failed to open Zyrex data file.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var rawLinks []string
	err = json.NewDecoder(file).Decode(&rawLinks)
	if err != nil {
		http.Error(w, "Failed to decode Zyrex data.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=zyrex_data.csv")
	w.Header().Set("Content-Type", "text/csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"npsn", "path"})

	for _, link := range rawLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		folderParam := u.Query().Get("folder")
		slashIndex := strings.Index(folderParam, "/")

		if slashIndex != -1 && len(folderParam) >= slashIndex+9 {
			npsn := folderParam[slashIndex+1 : slashIndex+9]
			writer.Write([]string{npsn, link})
		}
	}
}
// getHisenseHandler
// @Summary Get Hisense data
// @Description Retrieves and processes Hisense data from a local JSON file, grouping links by NPSN.
// @Tags Migrate Digit 2025
// @Produce json
// @Success 200 {object} map[string]interface{}
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /hisense [get]
func getHisenseHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	file, err := os.Open("public/hisense.json")
	if err != nil {
		http.Error(w, "Failed to open Hisense data file.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var rawLinks []string
	err = json.NewDecoder(file).Decode(&rawLinks)
	if err != nil {
		http.Error(w, "Failed to decode Hisense data.", http.StatusInternalServerError)
		return
	}

	groupedData := make(map[string][]string)

	for _, link := range rawLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		folderParam := u.Query().Get("folder")
		slashIndex := strings.Index(folderParam, "/")

		if slashIndex != -1 && len(folderParam) >= slashIndex+9 {
			npsn := folderParam[slashIndex+1 : slashIndex+9]
			groupedData[npsn] = append(groupedData[npsn], link)
		}
	}

	type NpsnData struct {
		Npsn string   `json:"npsn"`
		Path []string `json:"path"`
	}

	var resultData []NpsnData
	for npsn, paths := range groupedData {
		resultData = append(resultData, NpsnData{
			Npsn: npsn,
			Path: paths,
		})
	}

	response := map[string]interface{}{
		"status": "success",
		"data":   resultData,
	}

	json.NewEncoder(w).Encode(response)
}

// getHisenseCsvHandler
// @Summary Get Hisense data as CSV
// @Description Retrieves Hisense data from a local JSON file and returns it as a downloadable CSV file.
// @Tags Migrate Digit 2025
// @Produce text/csv
// @Success 200 {file} binary
// @Failure 405 {string} string "Method Not Allowed"
// @Failure 500 {string} string "Internal Server Error"
// @Router /hisense.csv [get]
func getHisenseCsvHandler(w http.ResponseWriter, r *http.Request) {
	enableCors(&w)
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed.", http.StatusMethodNotAllowed)
		return
	}

	file, err := os.Open("public/hisense.json")
	if err != nil {
		http.Error(w, "Failed to open Hisense data file.", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	var rawLinks []string
	err = json.NewDecoder(file).Decode(&rawLinks)
	if err != nil {
		http.Error(w, "Failed to decode Hisense data.", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Disposition", "attachment; filename=hisense_data.csv")
	w.Header().Set("Content-Type", "text/csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	writer.Write([]string{"npsn", "path"})

	for _, link := range rawLinks {
		u, err := url.Parse(link)
		if err != nil {
			continue
		}

		folderParam := u.Query().Get("folder")
		slashIndex := strings.Index(folderParam, "/")

		if slashIndex != -1 && len(folderParam) >= slashIndex+9 {
			npsn := folderParam[slashIndex+1 : slashIndex+9]
			writer.Write([]string{npsn, link})
		}
	}
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
	http.HandleFunc("/list", listHandler)
	http.HandleFunc("/list-folders", listFolderHandler)
	http.HandleFunc("/dac", getDacHandler)
	http.HandleFunc("/dac.csv", getDacCsvHandler)
	http.HandleFunc("/zyrex", getZyrexHandler)
	http.HandleFunc("/zyrex.csv", getZyrexCsvHandler)
	http.HandleFunc("/hisense", getHisenseHandler)
	http.HandleFunc("/hisense.csv", getHisenseCsvHandler)
	log.Println("Server is listening on port 8080.")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
