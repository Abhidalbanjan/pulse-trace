package models

// APIResponse is the standard envelope for all API responses.
type APIResponse struct {
	Success bool            `json:"success"`
	Data    interface{}     `json:"data,omitempty"`
	Error   string          `json:"error,omitempty"`
	Meta    *PaginationMeta `json:"meta,omitempty"`
}

// PaginationMeta carries pagination info in list responses.
type PaginationMeta struct {
	Page       int   `json:"page"`
	PageSize   int   `json:"page_size"`
	Total      int64 `json:"total"`
	TotalPages int   `json:"total_pages"`
}

// OK returns a successful response with data.
func OK(data interface{}) APIResponse {
	return APIResponse{Success: true, Data: data}
}

// OKPaginated returns a successful paginated response.
func OKPaginated(data interface{}, meta *PaginationMeta) APIResponse {
	return APIResponse{Success: true, Data: data, Meta: meta}
}

// Fail returns an error response.
func Fail(err string) APIResponse {
	return APIResponse{Success: false, Error: err}
}
