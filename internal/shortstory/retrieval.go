package shortstory

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// KnowledgeDoc 是短篇轻量资料检索的一条语料（场景事实、结构承诺、
// 人物知识、作者约束等）。
type KnowledgeDoc struct {
	ID   string
	Kind string
	Text string
}

// RetrieveKnowledge 用本地 BM25（英文词 + CJK bigram，无需词典与
// embedding）对语料排序，并按预算整条打包——不裁半条，避免把否定句
// 或条件句截断。移植自 Show Me The Story internal/story/knowledge_retrieval.go
// @ f790187（MIT），适配记录见 docs/dual-pipeline-porting-audit.md R2：
// 保留 BM25 排序与整条预算策略，去掉其设定实体/记忆章节模型，语料改为
// 短篇管线的扁平知识条目。
func RetrieveKnowledge(query string, docs []KnowledgeDoc, budgetRunes int) []KnowledgeDoc {
	if len(docs) == 0 || budgetRunes <= 0 {
		return nil
	}
	order := rankKnowledgeDocs(docs, query)
	selected := make([]KnowledgeDoc, 0, len(docs))
	for _, i := range order {
		cost := utf8.RuneCountInString(docs[i].Text) + 1
		if cost > budgetRunes {
			continue
		}
		selected = append(selected, docs[i])
		budgetRunes -= cost
	}
	return selected
}

// knowledgeTerms 分词：英文/数字按词，CJK 按相邻 bigram（无词典依赖）。
func knowledgeTerms(text string) map[string]int {
	terms := map[string]int{}
	var word []rune
	var previous rune
	flush := func() {
		if len(word) > 1 {
			terms[string(word)]++
		}
		word = word[:0]
	}
	for _, r := range strings.ToLower(text) {
		if unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Hangul, r) {
			flush()
			if previous != 0 {
				terms[string([]rune{previous, r})]++
			}
			previous = r
		} else {
			previous = 0
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				word = append(word, r)
			} else {
				flush()
			}
		}
	}
	flush()
	return terms
}

// rankKnowledgeDocs 是 BM25（k1≈1.2, b≈0.75 变体）：稳定并列保序，
// 保证同语料下提示词可复现。
func rankKnowledgeDocs(docs []KnowledgeDoc, query string) []int {
	queryTerms := knowledgeTerms(query)
	terms := make([]string, 0, len(queryTerms))
	for term := range queryTerms {
		terms = append(terms, term)
	}
	sort.Strings(terms)

	frequencies := make([]map[string]int, len(docs))
	lengths := make([]int, len(docs))
	df := map[string]int{}
	total := 0
	for i, doc := range docs {
		frequencies[i] = knowledgeTerms(doc.Text)
		for term, count := range frequencies[i] {
			lengths[i] += count
			if queryTerms[term] > 0 {
				df[term]++
			}
		}
		total += lengths[i]
	}
	average := float64(max(total, 1)) / float64(max(len(docs), 1))

	scores := make([]float64, len(docs))
	order := []int{}
	for i := range docs {
		score := 0.0
		for _, term := range terms {
			count := frequencies[i][term]
			if count == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(docs)-df[term])+0.5)/(float64(df[term])+0.5))
			tf := float64(count)
			score += idf * tf * 2.2 / (tf + 1.2*(0.25+0.75*float64(lengths[i])/average))
		}
		if score > 0 {
			scores[i] = score
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return scores[order[i]] > scores[order[j]] })
	return order
}
