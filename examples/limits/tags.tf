// Copyright (c) 2017, 2019, Oracle and/or its affiliates. All rights reserved.

resource "oci_identity_tag_namespace" "tag-namespace1" {
  #Required
  compartment_id = "${var.tenancy_ocid}"
  description    = "Just a test"
  name           = "testexamples-tag-namespace"
}

resource "oci_identity_tag" "tag1" {
  #Required
  description      = "tf example tag"
  name             = "tf-example-tag"
  tag_namespace_id = "${oci_identity_tag_namespace.tag-namespace1.id}"
}

resource "oci_identity_tag" "tag2" {
  #Required
  description      = "tf example tag 2"
  name             = "tf-example-tag-2"
  tag_namespace_id = "${oci_identity_tag_namespace.tag-namespace1.id}"
}
