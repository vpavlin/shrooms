cmake_minimum_required(VERSION 3.14)
project(ShroomsCorePlugin LANGUAGES CXX)

if(DEFINED ENV{LOGOS_MODULE_BUILDER_ROOT})
    include($ENV{LOGOS_MODULE_BUILDER_ROOT}/cmake/LogosModule.cmake)
elseif(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/cmake/LogosModule.cmake")
    include(cmake/LogosModule.cmake)
else()
    message(FATAL_ERROR "LogosModule.cmake not found")
endif()

# The module name comes from metadata.json, so there is one source of truth.
#
# Quoted, unlike the upstream template: an unquoted expansion splits the
# document on spaces and semicolons, so a description containing either turns
# into several arguments and the parse yields nothing. It fails as
# "logos_module: NAME is required", which points at the call rather than at the
# read that actually failed.
file(READ "${CMAKE_CURRENT_SOURCE_DIR}/metadata.json" METADATA_JSON)
string(JSON MODULE_NAME GET "${METADATA_JSON}" name)
if(NOT MODULE_NAME)
    message(FATAL_ERROR "could not read `name` from metadata.json")
endif()

logos_module(
    NAME ${MODULE_NAME}
    SOURCES
        src/shrooms_core_impl.h
        src/shrooms_core_impl.cpp
)
