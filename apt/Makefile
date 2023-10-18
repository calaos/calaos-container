.PHONY: libcalaos-apt.so

all: libcalaos-apt.so

CFLAGS = $(shell pkg-config --cflags glib-2.0)
LDLIBS = $(shell pkg-config --libs glib-2.0)

libcalaos-apt.so: apt.cpp apt-cache-file.cpp
	g++ -o $@ $^ $(CFLAGS) \
		-std=c++17 -O2 -fPIC -shared