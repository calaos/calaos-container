project('calaos-apt', 'cpp')

# Set the version of the project
version = '1.0.0'

# Set the C++ standard to use
cpp_std = 'c++17'

# Define the shared library target
libcalaos_apt = shared_library(
    'calaos-apt',
    sources: [
        'apt.cpp',
        'apt-cache-file.cpp',
    ],
    dependencies: [
        dependency('glib-2.0'),
        dependency('apt-pkg'),
    ],
    install: true,
    version: version,
)
