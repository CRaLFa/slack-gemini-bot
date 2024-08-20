package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/google/generative-ai-go/genai"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
)

func main() {
	flag.Parse()
	arg := flag.Arg(0)
	if arg == "" {
		log.Fatal("At least 1 argument needed")
	}

	if err := godotenv.Load(); err != nil {
		log.Fatal("Error loading .env file")
	}
	geminiApiKey := os.Getenv("GEMINI_API_KEY")

	ctx := context.Background()
	geminiClient, err := genai.NewClient(ctx, option.WithAPIKey(geminiApiKey))
	if err != nil {
		log.Fatal(err)
	}
	defer geminiClient.Close()

	model := geminiClient.GenerativeModel("gemini-1.5-flash")
	res, err := model.GenerateContent(ctx, genai.Text(arg))
	if err != nil {
		log.Fatal(err)
	}
	printResponse(res)
}

func printResponse(res *genai.GenerateContentResponse) {
	for _, cand := range res.Candidates {
		if cand != nil {
			for _, part := range cand.Content.Parts {
				fmt.Println(part)
			}
		}
	}
	fmt.Println("---")
}
