package billingclient

// Client talks to the billing service.
type Client struct {
	apiKey string
}

// NewClient creates a billing client with the embedded key.
func NewClient() *Client {
	return &Client{apiKey: "sk-live-9d8f7a6b5c4d3e2f1a0b"}
}