package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	_ "github.com/lib/pq"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// Organization from database
type Organization struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	SpreadsheetID string `json:"spreadsheet_id"`
}

// Appointment from database
type Appointment struct {
	ID                  int64     `json:"id"`
	ClientName          string    `json:"client_name"`
	ClientPhone         string    `json:"client_phone"`
	Master              string    `json:"master"`
	Service             string    `json:"service"`
	AppointmentDatetime time.Time `json:"appointment_datetime"`
	TotalAmount         float64   `json:"total_amount"`
	PaymentMethod       string    `json:"payment_method"`
	OrderStatus         string    `json:"order_status"`
}

// ExportRequest
type ExportRequest struct {
	Date string `json:"date"` // format: 2006-01-02
}

// ExportResponse
type ExportResponse struct {
	Success    bool   `json:"success"`
	Message    string `json:"message"`
	SheetName  string `json:"sheet_name,omitempty"`
	SheetURL   string `json:"sheet_url,omitempty"`
	RowsExport int    `json:"rows_exported,omitempty"`
}

var db *sql.DB
var sheetsService *sheets.Service

func main() {
	log.Println("🚀 chk-gsheet starting...")

	// Database connection
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		databaseURL = "postgresql://parser_user:parser_secure_password@postgres_prod:5432/sonline_parser?sslmode=disable"
	}

	var err error
	db, err = sql.Open("postgres", databaseURL)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer db.Close()

	if err = db.Ping(); err != nil {
		log.Fatalf("Failed to ping database: %v", err)
	}
	log.Println("✅ Connected to PostgreSQL")

	// Initialize Google Sheets API
	if err := initSheetsService(); err != nil {
		log.Fatalf("Failed to init Google Sheets: %v", err)
	}
	log.Println("✅ Google Sheets API initialized")

	// Router
	r := mux.NewRouter()
	r.HandleFunc("/", healthCheck).Methods("GET")
	r.HandleFunc("/api/health", healthCheck).Methods("GET")
	r.HandleFunc("/api/export/{org_id}", exportHandler).Methods("POST")

	// Server
	port := os.Getenv("PORT")
	if port == "" {
		port = "8002"
	}

	log.Printf("🌐 Server listening on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}

func healthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "ok",
		"service": "chk-gsheet",
	})
}

func initSheetsService() error {
	ctx := context.Background()

	// Try to read credentials from environment variable first
	credJSON := os.Getenv("GOOGLE_CREDENTIALS_JSON")
	if credJSON == "" {
		// Try to read from file
		credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
		if credFile == "" {
			credFile = "/app/credentials.json"
		}
		data, err := os.ReadFile(credFile)
		if err != nil {
			return fmt.Errorf("failed to read credentials: %w", err)
		}
		credJSON = string(data)
	}

	config, err := google.JWTConfigFromJSON([]byte(credJSON), sheets.SpreadsheetsScope)
	if err != nil {
		return fmt.Errorf("failed to parse credentials: %w", err)
	}

	client := config.Client(ctx)
	sheetsService, err = sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return fmt.Errorf("failed to create sheets service: %w", err)
	}

	return nil
}

func exportHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	orgID, err := strconv.ParseInt(vars["org_id"], 10, 64)
	if err != nil {
		sendError(w, http.StatusBadRequest, "Invalid organization ID")
		return
	}

	// Parse request body
	var req ExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Default to today
		req.Date = time.Now().Format("2006-01-02")
	}
	if req.Date == "" {
		req.Date = time.Now().Format("2006-01-02")
	}

	log.Printf("📤 Export request: org=%d, date=%s", orgID, req.Date)

	// Get organization
	org, err := getOrganization(orgID)
	if err != nil {
		log.Printf("❌ Organization not found: %v", err)
		sendError(w, http.StatusNotFound, "Organization not found")
		return
	}

	if org.SpreadsheetID == "" {
		sendError(w, http.StatusBadRequest, "No spreadsheet configured for this organization")
		return
	}

	// Get appointments for the date
	appointments, err := getAppointments(orgID, req.Date)
	if err != nil {
		log.Printf("❌ Failed to get appointments: %v", err)
		sendError(w, http.StatusInternalServerError, "Failed to get appointments")
		return
	}

	if len(appointments) == 0 {
		sendJSON(w, ExportResponse{
			Success: true,
			Message: "No appointments to export",
		})
		return
	}

	// Export to Google Sheets
	sheetName, err := exportToSheets(org, appointments, req.Date)
	if err != nil {
		log.Printf("❌ Export failed: %v", err)
		sendError(w, http.StatusInternalServerError, fmt.Sprintf("Export failed: %v", err))
		return
	}

	log.Printf("✅ Exported %d appointments to sheet '%s'", len(appointments), sheetName)

	sendJSON(w, ExportResponse{
		Success:    true,
		Message:    fmt.Sprintf("Exported to sheet '%s'", sheetName),
		SheetName:  sheetName,
		SheetURL:   fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s", org.SpreadsheetID),
		RowsExport: len(appointments),
	})
}

func getOrganization(id int64) (*Organization, error) {
	org := &Organization{}
	err := db.QueryRow(`
		SELECT id, name, COALESCE(spreadsheet_id, '')
		FROM organizations WHERE id = $1
	`, id).Scan(&org.ID, &org.Name, &org.SpreadsheetID)
	return org, err
}

func getAppointments(orgID int64, date string) ([]Appointment, error) {
	rows, err := db.Query(`
		SELECT id, COALESCE(client_name, ''), COALESCE(client_phone, ''),
		       COALESCE(master, ''), COALESCE(service, ''), appointment_datetime,
		       COALESCE(total_amount, 0), COALESCE(payment_method, ''), COALESCE(order_status, '')
		FROM appointments
		WHERE organization_id = $1 AND DATE(appointment_datetime) = $2
		ORDER BY master, appointment_datetime
	`, orgID, date)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var appointments []Appointment
	for rows.Next() {
		var apt Appointment
		err := rows.Scan(&apt.ID, &apt.ClientName, &apt.ClientPhone,
			&apt.Master, &apt.Service, &apt.AppointmentDatetime,
			&apt.TotalAmount, &apt.PaymentMethod, &apt.OrderStatus)
		if err != nil {
			return nil, err
		}
		appointments = append(appointments, apt)
	}
	return appointments, nil
}

func exportToSheets(org *Organization, appointments []Appointment, date string) (string, error) {
	ctx := context.Background()

	// Parse date for sheet name (format: 02.12.2025)
	parsedDate, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", fmt.Errorf("invalid date format: %w", err)
	}
	sheetName := parsedDate.Format("02.01.2006")

	// 1. Find "Пример" sheet to copy
	spreadsheet, err := sheetsService.Spreadsheets.Get(org.SpreadsheetID).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to get spreadsheet: %w", err)
	}

	var templateSheetID int64 = -1
	var existingSheetID int64 = -1
	for _, sheet := range spreadsheet.Sheets {
		if sheet.Properties.Title == "Пример" {
			templateSheetID = sheet.Properties.SheetId
		}
		if sheet.Properties.Title == sheetName {
			existingSheetID = sheet.Properties.SheetId
		}
	}

	if templateSheetID == -1 {
		return "", fmt.Errorf("template sheet 'Пример' not found")
	}

	// 2. If sheet with this date exists, clear it. Otherwise, copy template.
	var targetSheetID int64
	if existingSheetID != -1 {
		// Clear existing sheet
		targetSheetID = existingSheetID
		log.Printf("📋 Sheet '%s' exists, clearing...", sheetName)
	} else {
		// Copy template sheet
		copyReq := &sheets.CopySheetToAnotherSpreadsheetRequest{
			DestinationSpreadsheetId: org.SpreadsheetID,
		}
		copyResp, err := sheetsService.Spreadsheets.Sheets.CopyTo(
			org.SpreadsheetID, templateSheetID, copyReq,
		).Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("failed to copy template: %w", err)
		}
		targetSheetID = copyResp.SheetId

		// Rename the copied sheet
		renameReq := &sheets.BatchUpdateSpreadsheetRequest{
			Requests: []*sheets.Request{
				{
					UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
						Properties: &sheets.SheetProperties{
							SheetId: targetSheetID,
							Title:   sheetName,
						},
						Fields: "title",
					},
				},
			},
		}
		_, err = sheetsService.Spreadsheets.BatchUpdate(org.SpreadsheetID, renameReq).Context(ctx).Do()
		if err != nil {
			return "", fmt.Errorf("failed to rename sheet: %w", err)
		}
		log.Printf("📋 Created new sheet '%s'", sheetName)
	}

	// 3. Group appointments by master
	byMaster := make(map[string][]Appointment)
	for _, apt := range appointments {
		master := apt.Master
		if master == "" {
			master = "Без мастера"
		}
		byMaster[master] = append(byMaster[master], apt)
	}

	// 4. Write date to B1
	dateFormatted := parsedDate.Format("02.01.2006")
	_, err = sheetsService.Spreadsheets.Values.Update(
		org.SpreadsheetID,
		fmt.Sprintf("'%s'!B1", sheetName),
		&sheets.ValueRange{Values: [][]interface{}{{dateFormatted}}},
	).ValueInputOption("USER_ENTERED").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to write date: %w", err)
	}

	// 5. Get masters list from organization (order matters)
	masters, err := getMasters(org.ID)
	if err != nil {
		log.Printf("⚠️ Could not get masters list, using from appointments: %v", err)
		// Fallback: use masters from appointments
		for m := range byMaster {
			masters = append(masters, m)
		}
	}

	// 6. Write data for each master
	// Each master block is 14 columns wide (B-O, P-AC, AD-AQ...)
	// Master 0: starts at column B (index 1)
	// Master 1: starts at column P (index 15)
	// Master N: starts at column 1 + N*14
	const masterBlockWidth = 14
	const startCol = 1 // Column B (0-indexed: A=0, B=1)
	const dataStartRow = 4
	const dataEndRow = 27
	const maxRecordsPerMaster = dataEndRow - dataStartRow + 1 // 24 records

	totalWritten := 0

	for masterIdx, masterName := range masters {
		apts, ok := byMaster[masterName]
		if !ok {
			continue // No appointments for this master
		}

		// Calculate column offset for this master
		colOffset := startCol + masterIdx*masterBlockWidth

		// Write master name to row 2
		masterNameCell := fmt.Sprintf("'%s'!%s2", sheetName, colToLetter(colOffset))
		_, err = sheetsService.Spreadsheets.Values.Update(
			org.SpreadsheetID,
			masterNameCell,
			&sheets.ValueRange{Values: [][]interface{}{{masterName}}},
		).ValueInputOption("USER_ENTERED").Context(ctx).Do()
		if err != nil {
			log.Printf("⚠️ Failed to write master name: %v", err)
		}

		// Prepare data arrays
		var timeValues [][]interface{}
		var sumValues [][]interface{}
		var paymentValues [][]interface{}

		for i, apt := range apts {
			if i >= maxRecordsPerMaster {
				break // Max 24 records per master
			}
			timeStr := apt.AppointmentDatetime.Format("15:04")
			payment := formatPaymentMethod(apt.PaymentMethod)

			timeValues = append(timeValues, []interface{}{timeStr})
			sumValues = append(sumValues, []interface{}{apt.TotalAmount})
			paymentValues = append(paymentValues, []interface{}{payment})
			totalWritten++
		}

		// Write time (column offset + 0)
		if len(timeValues) > 0 {
			timeRange := fmt.Sprintf("'%s'!%s%d:%s%d", sheetName,
				colToLetter(colOffset), dataStartRow,
				colToLetter(colOffset), dataStartRow+len(timeValues)-1)
			_, err = sheetsService.Spreadsheets.Values.Update(
				org.SpreadsheetID, timeRange,
				&sheets.ValueRange{Values: timeValues},
			).ValueInputOption("USER_ENTERED").Context(ctx).Do()
			if err != nil {
				log.Printf("⚠️ Failed to write time: %v", err)
			}
		}

		// Write sum (column offset + 2, i.e. D for master 0)
		if len(sumValues) > 0 {
			sumRange := fmt.Sprintf("'%s'!%s%d:%s%d", sheetName,
				colToLetter(colOffset+2), dataStartRow,
				colToLetter(colOffset+2), dataStartRow+len(sumValues)-1)
			_, err = sheetsService.Spreadsheets.Values.Update(
				org.SpreadsheetID, sumRange,
				&sheets.ValueRange{Values: sumValues},
			).ValueInputOption("USER_ENTERED").Context(ctx).Do()
			if err != nil {
				log.Printf("⚠️ Failed to write sum: %v", err)
			}
		}

		// Write payment method (column offset + 10, i.e. L for master 0)
		if len(paymentValues) > 0 {
			paymentRange := fmt.Sprintf("'%s'!%s%d:%s%d", sheetName,
				colToLetter(colOffset+10), dataStartRow,
				colToLetter(colOffset+10), dataStartRow+len(paymentValues)-1)
			_, err = sheetsService.Spreadsheets.Values.Update(
				org.SpreadsheetID, paymentRange,
				&sheets.ValueRange{Values: paymentValues},
			).ValueInputOption("USER_ENTERED").Context(ctx).Do()
			if err != nil {
				log.Printf("⚠️ Failed to write payment: %v", err)
			}
		}

		log.Printf("📝 Master '%s': wrote %d appointments", masterName, len(apts))
	}

	return sheetName, nil
}

// colToLetter converts 0-indexed column number to letter (0=A, 1=B, 26=AA)
func colToLetter(col int) string {
	result := ""
	for col >= 0 {
		result = string(rune('A'+col%26)) + result
		col = col/26 - 1
	}
	return result
}

// getMasters gets ordered list of masters for organization
func getMasters(orgID int64) ([]string, error) {
	var mastersStr string
	err := db.QueryRow(`
		SELECT COALESCE(array_to_string(masters, ','), '')
		FROM organizations WHERE id = $1
	`, orgID).Scan(&mastersStr)
	if err != nil {
		return nil, err
	}

	var masters []string
	for _, m := range strings.Split(mastersStr, ",") {
		m = strings.TrimSpace(m)
		if m != "" {
			masters = append(masters, m)
		}
	}
	return masters, nil
}

func formatPaymentMethod(method string) string {
	switch method {
	case "cash":
		return "Наличные"
	case "card":
		return "Карта"
	case "transfer":
		return "Перевод"
	default:
		return "Не указан"
	}
}

func sendError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ExportResponse{
		Success: false,
		Message: message,
	})
}

func sendJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// rebuild trigger
