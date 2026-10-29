package main

import (
	"log"

	"gatekeeper/services/internal/echo"
)

func main() {
	if err := echo.Run("auth-service"); err != nil {
		log.Fatal(err)
	}
}
