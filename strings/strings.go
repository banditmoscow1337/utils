package strings

import (
	"bytes"
	"math/rand"
	"regexp"
	"strconv"
)

func StringToInt(str string) (i int, err error) {
	var chr string
	for _, c := range str {
		chr += strconv.Itoa(int(c))
	}
	return strconv.Atoi(chr)
}

func FromByteSearch(data *[]byte, sepIn string, offIn int, sepOut string, offOut int) ([]byte, string, bool) {
	inBt := []byte(sepIn)
	if offIn == 0 {
		offIn = len(inBt)
	}
	if index := bytes.Index(*data, inBt); index != -1 {
		*data = (*data)[index+offIn:]
		if sepOut == "" {
			return []byte{}, "", true
		}
		if index = bytes.Index(*data, []byte(sepOut)); index != -1 {
			r := (*data)[:index+offOut]
			return r, string(r), true
		}
	}
	return []byte{}, "", false
}

func Shuffle(str string) string {
	inRune := []rune(str)
	rand.Shuffle(len(inRune), func(i, j int) {
		inRune[i], inRune[j] = inRune[j], inRune[i]
	})

	return string(inRune)
}

func Reverse(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

func FindByte(data, toFind, toFindEnd *[]byte, sz, cut int) string {
	ind := bytes.Index(*data, *toFind)
	if ind == -1 || (cut > 0 && ind > cut) {
		return ""
	}
	*data = (*data)[ind+sz:]
	return string((*data)[:bytes.Index(*data, *toFindEnd)])
}

func RemoveRedundantSpaces(input string) string {
	re_leadclose_whtsp := regexp.MustCompile(`^[\s\p{Zs}]+|[\s\p{Zs}]+$`)
	re_inside_whtsp := regexp.MustCompile(`[\s\p{Zs}]{2,}`)
	final := re_leadclose_whtsp.ReplaceAllString(input, "")
	return re_inside_whtsp.ReplaceAllString(final, " ")
}
