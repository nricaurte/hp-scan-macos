/* SOAPHT capabilities from GetScannerElements, independent of model names.
 * XML uses local element names: namespace prefixes are not protocol constants.
 * No DTDs, external entities, network access or parser recovery are permitted. */
#ifndef SOAPHT_CAPABILITIES_H
#define SOAPHT_CAPABILITIES_H
#include <libxml/parser.h>
#include <libxml/tree.h>

struct source_caps {
    int present, min_width, min_height, max_width, max_height;
    int optical, gray, color;
    SANE_Int resolutions[MAX_LIST_SIZE];
};
struct scanner_caps {
    struct source_caps platen, adf;
    int adf_duplex;
};

static xmlDocPtr scanner_xml(const char *data, size_t size)
{
    if (!data || !size || size > 256 * 1024 || memchr(data, 0, size) ||
        strstr(data, "<!DOCTYPE") || strstr(data, "<!ENTITY")) return NULL;
    xmlDocPtr doc = xmlReadMemory(data, (int)size, "soapht.xml", NULL,
        XML_PARSE_NONET | XML_PARSE_NOERROR | XML_PARSE_NOWARNING);
    if (doc && (doc->intSubset || doc->extSubset)) { xmlFreeDoc(doc); return NULL; }
    return doc;
}

static xmlNodePtr child_named(xmlNodePtr parent, const char *name)
{
    for (xmlNodePtr n = parent ? parent->children : NULL; n; n = n->next)
        if (n->type == XML_ELEMENT_NODE && xmlStrEqual(n->name, BAD_CAST name)) return n;
    return NULL;
}

static xmlNodePtr descendant_named(xmlNodePtr node, const char *name, int depth)
{
    if (depth > 32) return NULL;
    for (xmlNodePtr n = node; n; n = n->next) {
        if (n->type != XML_ELEMENT_NODE) continue;
        if (xmlStrEqual(n->name, BAD_CAST name)) return n;
        xmlNodePtr found = descendant_named(n->children, name, depth + 1);
        if (found) return found;
    }
    return NULL;
}

static int node_text(xmlNodePtr node, char *out, size_t capacity)
{
    if (!node) return 1;
    for (xmlNodePtr c = node->children; c; c = c->next)
        if (c->type == XML_ELEMENT_NODE || c->type == XML_ENTITY_REF_NODE) return 1;
    xmlChar *value = xmlNodeGetContent(node);
    if (!value) return 1;
    const char *start = (const char *)value;
    while (isspace((unsigned char)*start)) start++;
    size_t len = strlen(start);
    while (len && isspace((unsigned char)start[len - 1])) len--;
    int bad = len == 0 || len >= capacity;
    if (!bad) { memcpy(out, start, len); out[len] = 0; }
    xmlFree(value);
    return bad;
}

static int node_positive(xmlNodePtr node, int *value)
{
    char text[32], *end;
    if (node_text(node, text, sizeof(text))) return 1;
    errno = 0;
    long n = strtol(text, &end, 10);
    if (errno == ERANGE || end == text || *end || n <= 0 || n > INT_MAX) return 1;
    *value = (int)n;
    return 0;
}

/* Missing/invalid booleans are distinguishable from explicit false. */
static int node_bool(xmlNodePtr node)
{
    char text[16];
    if (node_text(node, text, sizeof(text))) return -1;
    if (!strcmp(text, "true") || !strcmp(text, "1")) return 1;
    if (!strcmp(text, "false") || !strcmp(text, "0")) return 0;
    return -1;
}

static int tag_int(const char *xml, const char *tag, int *value)
{
    xmlDocPtr doc = scanner_xml(xml, strlen(xml));
    if (!doc) return 1;
    int status = node_positive(descendant_named(xmlDocGetRootElement(doc), tag, 0), value);
    xmlFreeDoc(doc);
    return status;
}

static int xml_boolean(const char *xml, const char *tag)
{
    xmlDocPtr doc = scanner_xml(xml, strlen(xml));
    if (!doc) return -1;
    int value = node_bool(descendant_named(xmlDocGetRootElement(doc), tag, 0));
    xmlFreeDoc(doc);
    return value;
}

/* Exact device status observed on M127fn after an interrupted ADF page. */
static int scanner_media_jam(const char *xml)
{
    xmlDocPtr doc = scanner_xml(xml, strlen(xml));
    if (!doc) return 0;
    char reason[64];
    int jammed = !node_text(descendant_named(xmlDocGetRootElement(doc),
                                           "ScannerStateReason", 0),
                            reason, sizeof(reason)) && !strcmp(reason, "MediaJam");
    xmlFreeDoc(doc);
    return jammed;
}

static int list_has(xmlNodePtr list, const char *value)
{
    for (xmlNodePtr n = list ? list->children : NULL; n; n = n->next) {
        char text[64];
        if (n->type == XML_ELEMENT_NODE && !node_text(n, text, sizeof(text)) &&
            !strcmp(text, value)) return 1;
    }
    return 0;
}

static int read_size(xmlNodePtr source, const char *name, int *width, int *height)
{
    xmlNodePtr size = child_named(source, name);
    if (node_positive(child_named(size, "Width"), width) ||
        node_positive(child_named(size, "Height"), height)) return 1;
    /* Bound fixed-point conversion and unsupported huge raster allocations. */
    return *width > 20000 || *height > 20000;
}

static int read_source(xmlNodePtr node, int adf, struct source_caps *caps,
                       xmlNodePtr inherited_colors)
{
    if (!node) return SANE_STATUS_GOOD; /* Source absent. */
    int supported = node_bool(child_named(node, adf ? "ADFSupported" : "FlatbedSupported"));
    if (supported < 0) return SANE_STATUS_IO_ERROR;
    if (!supported) return SANE_STATUS_GOOD;
    caps->present = 1;
    int x, y;
    if (read_size(node, adf ? "ADFMinimumSize" : "PlatenMinimumSize", &caps->min_width, &caps->min_height) ||
        read_size(node, adf ? "ADFMaximumSize" : "PlatenMaximumSize", &caps->max_width, &caps->max_height) ||
        read_size(node, adf ? "ADFOpticalResolution" : "PlatenOpticalResolution", &x, &y) ||
        caps->min_width > caps->max_width || caps->min_height >= caps->max_height)
        return SANE_STATUS_IO_ERROR;
    caps->optical = x < y ? x : y;
    /* Existing JPEG pipeline policy: 150/300/600 only, limited by the source.
     * Optical maxima alone are not proof that arbitrary intermediate DPI work. */
    const int choices[] = {150, 300, 600};
    for (size_t i = 0; i < sizeof(choices) / sizeof(choices[0]); i++)
        if (choices[i] <= caps->optical)
            caps->resolutions[++caps->resolutions[0]] = choices[i];
    if (!caps->resolutions[0]) return SANE_STATUS_UNSUPPORTED;
    xmlNodePtr colors = child_named(node, "ColorSupported");
    if (!colors) colors = inherited_colors; /* M127's ADF omits its color list. */
    caps->gray = list_has(colors, "GrayScale8");
    caps->color = list_has(colors, "RGB24");
    return (caps->gray || caps->color) ? SANE_STATUS_GOOD : SANE_STATUS_UNSUPPORTED;
}

static int parse_capabilities(const char *xml, size_t size, struct scanner_caps *caps)
{
    memset(caps, 0, sizeof(*caps));
    xmlDocPtr doc = scanner_xml(xml, size);
    if (!doc) return SANE_STATUS_IO_ERROR;
    int status = SANE_STATUS_IO_ERROR;
    xmlNodePtr config = descendant_named(xmlDocGetRootElement(doc), "ScannerConfiguration", 0);
    if (!config) goto done;
    xmlNodePtr settings = child_named(config, "DeviceSettings");
    if (!list_has(child_named(settings, "FormatSupported"), "jfif")) {
        status = SANE_STATUS_UNSUPPORTED;
        goto done;
    }
    xmlNodePtr platen = child_named(config, "Platen"), adf = child_named(config, "ADF");
    xmlNodePtr colors = child_named(platen, "ColorSupported");
    if (!colors) colors = child_named(settings, "ColorSupported");
    status = read_source(platen, 0, &caps->platen, colors);
    if (status) goto done;
    status = read_source(adf, 1, &caps->adf, colors);
    if (status) goto done;
    caps->adf_duplex = node_bool(child_named(adf, "ADFSupportsDuplex")) == 1;
    if (!caps->platen.present && !caps->adf.present) status = SANE_STATUS_UNSUPPORTED;
    /* The current HPLIP ABI has one mode list. Advertise the safe intersection. */
    if (caps->platen.present && caps->adf.present &&
        !(caps->platen.gray && caps->adf.gray) && !(caps->platen.color && caps->adf.color))
        status = SANE_STATUS_UNSUPPORTED;
 done:
    xmlFreeDoc(doc);
    return status;
}
#endif
