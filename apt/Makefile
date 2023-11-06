.PHONY: libcalaos-apt.so

all:
	@rm -fr build
	meson setup build --prefix=/usr
	meson compile -C build
	meson install -C build
