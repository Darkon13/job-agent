package hhapi

// nativeUserAgent — стабильная личность Android-приложения. UUID и модель
// не меняются между запусками, иначе HH считает устройства подозрительными.
func nativeUserAgent() string {
	return "ru.hh.android/7.144.13722, Device: C5PKOM6TX3, Android OS: 11 (UUID: 6e968ac9-3103-40a9-a3b8-fbca16aa3c97)"
}
