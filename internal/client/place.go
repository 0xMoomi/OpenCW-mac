package client

// cities names the US Cloudflare data centres by IATA code. A trace through
// the exit reports the one that answered, which is the large city nearest
// the exit; that is enough to tell servers apart without another lookup.
var cities = map[string]string{
	"ABQ": "앨버커키", "ANC": "앵커리지", "ATL": "애틀랜타", "AUS": "오스틴",
	"BGR": "뱅고어", "BNA": "내슈빌", "BOS": "보스턴", "BUF": "버펄로",
	"CHS": "찰스턴", "CLE": "클리블랜드", "CLT": "샬럿", "CMH": "콜럼버스",
	"DEN": "덴버", "DFW": "댈러스", "DTW": "디트로이트", "EWR": "뉴어크",
	"FSD": "수폴스", "HNL": "호놀룰루", "IAD": "애슈번", "IAH": "휴스턴",
	"IND": "인디애나폴리스", "JAX": "잭슨빌", "LAS": "라스베이거스", "LAX": "로스앤젤레스",
	"MCI": "캔자스시티", "MEM": "멤피스", "MFE": "매캘런", "MIA": "마이애미",
	"MSP": "미니애폴리스", "OKC": "오클라호마시티", "OMA": "오마하", "ORD": "시카고",
	"ORF": "노퍽", "PDX": "포틀랜드", "PHL": "필라델피아", "PHX": "피닉스",
	"PIT": "피츠버그", "RDU": "롤리", "RIC": "리치먼드", "SAN": "샌디에이고",
	"SAT": "샌안토니오", "SEA": "시애틀", "SJC": "새너제이", "SLC": "솔트레이크시티",
	"SMF": "새크라멘토", "STL": "세인트루이스", "TLH": "탤러해시", "TPA": "탬파",
}

// placeLabel is "미국 로스앤젤레스", "미국" for an unknown US city, or the
// country code elsewhere (exits are US only, so that is a fallback).
func placeLabel(country, colo string) string {
	if country != "US" {
		return country
	}
	if city, ok := cities[colo]; ok {
		return "미국 " + city
	}
	return "미국"
}
