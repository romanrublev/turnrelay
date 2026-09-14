package vk

import "math/rand/v2"

// randomName returns an ordinary "<First> <Last>" name in plain Latin
// script. VK shows this name to other call participants for the anonymous
// join, so it should look like a normal display name.
var firstNames = []string{
	"Alex", "Maria", "Ivan", "Anna", "Dmitri",
	"Olga", "Sergei", "Elena", "Pavel", "Tatiana",
	"Nikolai", "Irina", "Andrei", "Svetlana", "Mikhail",
	"Natalia", "Viktor", "Yulia", "Igor", "Ekaterina",
}

var lastNames = []string{
	"Ivanov", "Petrova", "Smirnov", "Kuznetsova", "Popov",
	"Sokolova", "Volkov", "Morozova", "Fedorov", "Vasilieva",
	"Novikov", "Andreeva", "Sorokin", "Belova", "Zaitsev",
	"Orlova", "Bogdanov", "Kalinina", "Makarov", "Titova",
}

func randomName() string {
	first := firstNames[rand.IntN(len(firstNames))]
	last := lastNames[rand.IntN(len(lastNames))]
	return first + " " + last
}
