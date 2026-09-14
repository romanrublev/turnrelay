package vk

import "math/rand/v2"

// randomName returns an ordinary "<First> <Last>" name in plain Latin
// script. VK shows this name to other call participants for the anonymous
// join, so it should look like a normal display name.
// Cyrillic to match what the real VK Calls iOS client sends as anonymName
// (e.g. "Марк Степанов", "Елизавета Морозова"), a masquerade data point.
var firstNames = []string{
	"Александр", "Мария", "Иван", "Анна", "Дмитрий",
	"Ольга", "Сергей", "Елена", "Павел", "Татьяна",
	"Николай", "Ирина", "Андрей", "Светлана", "Михаил",
	"Наталья", "Виктор", "Юлия", "Игорь", "Екатерина",
}

var lastNames = []string{
	"Иванов", "Петрова", "Смирнов", "Кузнецова", "Попов",
	"Соколова", "Волков", "Морозова", "Фёдоров", "Васильева",
	"Новиков", "Андреева", "Сорокин", "Белова", "Зайцев",
	"Орлова", "Богданов", "Калинина", "Макаров", "Титова",
}

func randomName() string {
	first := firstNames[rand.IntN(len(firstNames))]
	last := lastNames[rand.IntN(len(lastNames))]
	return first + " " + last
}
