#include <string.h>

#define BITOP(a,b,op) \
 ((a)[(size_t)(b)/(8*sizeof *(a))] op (size_t)1<<((size_t)(b)%(8*sizeof *(a))))

size_t strcspn(const char *s, const char *c)
{
	const char *a = s;
	size_t byteset[32/sizeof(size_t)];

	if (!c[0] || !c[1]) return __strchrnul(s, *c)-a;
	if (!c[2]) {
		/* Two bytes to stop at, as SQLite's LIKE asks at each place a
		 * match can start: two compares a byte, and no set to fill. */
		char c0 = c[0], c1 = c[1];
		for (; *s && *s != c0 && *s != c1; s++);
		return s-a;
	}

	memset(byteset, 0, sizeof byteset);
	for (; *c && BITOP(byteset, *(unsigned char *)c, |=); c++);
	for (; *s && !BITOP(byteset, *(unsigned char *)s, &); s++);
	return s-a;
}
