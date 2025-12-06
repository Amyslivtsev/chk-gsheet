# chk-gsheet

Google Sheets export service for CHK Admin.

## API

### POST /api/export/{org_id}

Export appointments to Google Sheets.

**Request body:**
```json
{
  "date": "2025-12-02"  // optional, defaults to today
}
```

**Response:**
```json
{
  "success": true,
  "message": "Exported to sheet '02.12.2025'",
  "sheet_name": "02.12.2025",
  "sheet_url": "https://docs.google.com/spreadsheets/d/...",
  "rows_exported": 25
}
```

## Environment Variables

- `DATABASE_URL` - PostgreSQL connection string
- `GOOGLE_CREDENTIALS_JSON` - Service Account JSON (as string)
- `PORT` - Server port (default: 8002)

## Deployment

Deployed via GitHub Actions to k3s cluster.

### Create Google credentials secret:

```bash
kubectl create secret generic google-credentials \
  --from-file=credentials.json=/path/to/credentials.json \
  -n prod
```

# rebuild
