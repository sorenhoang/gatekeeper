package main

import (
	"log"

	"gatekeeper/services/internal/echo"
)

func main() {
	if err := echo.Run("user-service"); err != nil {
		log.Fatal(err)
	}
}
