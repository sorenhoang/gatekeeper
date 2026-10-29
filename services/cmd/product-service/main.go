package main

import (
	"log"

	"gatekeeper/services/internal/echo"
)

func main() {
	if err := echo.Run("product-service"); err != nil {
		log.Fatal(err)
	}
}
