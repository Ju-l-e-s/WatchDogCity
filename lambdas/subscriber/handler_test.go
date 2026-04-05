package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateEmail(t *testing.T) {
	assert.True(t, isValidEmail("user@example.com"))
	assert.True(t, isValidEmail("user+tag@sub.domain.fr"))
	assert.False(t, isValidEmail("notanemail"))
	assert.False(t, isValidEmail("missing@tld"))
	assert.False(t, isValidEmail(""))
	assert.False(t, isValidEmail("@domain.com"))
}

func TestCORSHeaders(t *testing.T) {
	headers := corsHeaders()
	assert.Equal(t, "*", headers["Access-Control-Allow-Origin"])
	assert.Equal(t, "POST,GET,OPTIONS", headers["Access-Control-Allow-Methods"])
}
