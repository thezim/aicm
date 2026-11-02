package model

import "time"

// Colors are the TUI colors. Empty values inherit from the next level:
// built-in defaults, then app.colors, then accounts.<id>.colors.
type Colors struct {
	ListForeground           string `yaml:"list_foreground" json:"list_foreground"`
	ListBackground           string `yaml:"list_background" json:"list_background"`
	SelectedForeground       string `yaml:"selected_foreground" json:"selected_foreground"`
	SelectedBackground       string `yaml:"selected_background" json:"selected_background"`
	HeaderForeground         string `yaml:"header_foreground" json:"header_foreground"`
	HeaderBackground         string `yaml:"header_background" json:"header_background"`
	StatusForeground         string `yaml:"status_foreground" json:"status_foreground"`
	StatusBackground         string `yaml:"status_background" json:"status_background"`
	TransientForeground      string `yaml:"transient_foreground" json:"transient_foreground"`
	TransientBackground      string `yaml:"transient_background" json:"transient_background"`
	TransientErrorForeground string `yaml:"transient_error_foreground" json:"transient_error_foreground"`
	KeysForeground           string `yaml:"keys_foreground" json:"keys_foreground"`
	KeysBackground           string `yaml:"keys_background" json:"keys_background"`
	HelpForeground           string `yaml:"help_foreground" json:"help_foreground"`
	HelpBackground           string `yaml:"help_background" json:"help_background"`
}

// RDPOptions is the curated set of common RDP options.
type RDPOptions struct {
	Username     string `yaml:"username" json:"username"`
	Domain       string `yaml:"domain" json:"domain"`
	RemotePort   int    `yaml:"remote_port" json:"remote_port"`
	FullScreen   bool   `yaml:"full_screen" json:"full_screen"`
	Resolution   string `yaml:"resolution" json:"resolution"`
	MultiMonitor bool   `yaml:"multimonitor" json:"multimonitor"`
	Clipboard    bool   `yaml:"clipboard" json:"clipboard"`
	Drives       bool   `yaml:"drives" json:"drives"`
	Printers     bool   `yaml:"printers" json:"printers"`
	Smartcards   bool   `yaml:"smartcards" json:"smartcards"`
	USBDevices   bool   `yaml:"usb_devices" json:"usb_devices"`
	Webcam       bool   `yaml:"webcam" json:"webcam"`
	Audio        string `yaml:"audio" json:"audio"` // local, remote, none
	Microphone   bool   `yaml:"microphone" json:"microphone"`
}

// Credentials is a short-lived AWS credential context sent to the daemon for
// one AWS CLI child. It is never persisted or logged.
type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
}

type ConnectionState string

const (
	StateStarting ConnectionState = "starting"
	StateReady    ConnectionState = "ready"
	StateFailed   ConnectionState = "failed"
	StateClosing  ConnectionState = "closing"
)

// Connection is the public, credential-free view of a tunnel.
type Connection struct {
	ID           string          `json:"id"`
	AccountID    string          `json:"account_id"`
	InstanceID   string          `json:"instance_id"`
	InstanceName string          `json:"instance_name,omitempty"`
	Region       string          `json:"region"`
	LocalPort    int             `json:"local_port"`
	State        ConnectionState `json:"state"`
	RDPPath      string          `json:"rdp_path,omitempty"`
	RDPOpened    bool            `json:"rdp_opened"`
	RDPError     string          `json:"rdp_error,omitempty"`
	Error        string          `json:"error,omitempty"`
	OutputTail   string          `json:"output_tail,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	ReadyAt      *time.Time      `json:"ready_at,omitempty"`
}

type DaemonInfo struct {
	Version       string    `json:"version"`
	Protocol      int       `json:"protocol"`
	StartedAt     time.Time `json:"started_at"`
	Connections   int       `json:"connections"`
	Clients       int       `json:"clients"`
	IdleExitAfter string    `json:"idle_exit_after"`
}
