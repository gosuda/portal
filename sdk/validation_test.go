package sdk

import "testing"

func TestIsURLSafeName(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// Valid names
		{"empty string", "", true},
		{"simple name", "my-service", true},
		{"with underscore", "my_service", true},
		{"with numbers", "service123", true},
		{"mixed case", "MyService", true},
		{"all hyphens", "my-cool-service", true},
		{"all underscores", "my_cool_service", true},
		{"alphanumeric only", "service", true},
		{"numbers only", "12345", true},
		{"korean", "한글서비스", true},
		{"korean with hyphen", "한글-서비스", true},
		{"korean with underscore", "한글_서비스", true},
		{"mixed korean english", "MyService한글", true},
		{"japanese", "日本語サービス", true},
		{"chinese", "中文服务", true},
		{"arabic", "خدمة", true},
		{"mixed languages", "Service-서비스-サービス", true},
		{"korean numbers", "서비스123", true},

		// Invalid names
		{"with space", "my service", false},
		{"with slash", "my/service", false},
		{"with dot", "my.service", false},
		{"with colon", "my:service", false},
		{"with question mark", "my?service", false},
		{"with ampersand", "my&service", false},
		{"with equals", "my=service", false},
		{"with percent", "my%service", false},
		{"with plus", "my+service", false},
		{"with asterisk", "my*service", false},
		{"with at", "my@service", false},
		{"with hash", "my#service", false},
		{"with exclamation", "my!service", false},
		{"with parentheses", "my(service)", false},
		{"with brackets", "my[service]", false},
		{"with braces", "my{service}", false},
		{"with semicolon", "my;service", false},
		{"with comma", "my,service", false},
		{"with quote", "my'service", false},
		{"with double quote", "my\"service", false},
		{"with backslash", "my\\service", false},
		{"with pipe", "my|service", false},
		{"with tilde", "my~service", false},
		{"with backtick", "my`service", false},
		{"with less than", "my<service", false},
		{"with greater than", "my>service", false},
		{"emoji", "my-service😀", false},
		{"with space korean", "한글 서비스", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isURLSafeName(tt.input)
			if result != tt.expected {
				t.Errorf("isURLSafeName(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}
