#pragma once

#ifdef __cplusplus
extern "C" {
#endif

typedef struct
{
    char *name;
    char *version_current;
    char *version_new;
} Pkg;

void aptInit();
void *aptCacheGetArray();
int aptCacheArrayCount(void *arr);
Pkg *aptCacheArrayGet(void *arr, int idx);
void aptCacheArrayFree(void *arr);

#ifdef __cplusplus
}  // extern "C"
#endif