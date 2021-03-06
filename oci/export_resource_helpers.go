package oci

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	oci_core "github.com/oracle/oci-go-sdk/core"
	oci_identity "github.com/oracle/oci-go-sdk/identity"
	oci_load_balancer "github.com/oracle/oci-go-sdk/loadbalancer"
)

type TerraformResourceHints struct {
	// Information about this resource
	resourceClass        string // The name of the resource class (e.g. oci_core_vcn)
	resourceAbbreviation string // An abbreviated version of the resource class used for generating shorter resource names (e.g. vcn)

	// Hints to help with discovering this resource using data sources
	datasourceClass              string                             // The name of the data source class to use for discovering resources (e.g. oci_core_vcns)
	datasourceItemsAttr          string                             // The attribute with the data source that contains the discovered resources returned by the data source (e.g. virtual_networks)
	requireResourceRefresh       bool                               // Whether to use the resource to fill in missing information from datasource (e.g. when datasources only return summary information)
	discoverableLifecycleStates  []string                           // List of lifecycle states that should be discovered. If empty, then all lifecycle states are discoverable.
	processDiscoveredResourcesFn ProcessOCIResourcesFunc            // Custom function for processing resources discovered by the data source
	alwaysExportable             bool                               // Some resources always need to be exportable, regardless of whether they are being targeted for export
	getIdFn                      func(*OCIResource) (string, error) // If the resource has no OCID generated by services, then implement this to generate one from the OCIResource. Typically used for composite IDs.

	// Override function for discovering resources. To be used when there is no datasource implementation to help with discovery.
	findResourcesOverrideFn func(*OracleClients, *TerraformResourceAssociation, *OCIResource) ([]*OCIResource, error)

	// Hints to help with generating HCL representation from this resource
	getHCLStringOverrideFn func(*strings.Builder, *OCIResource, map[string]string) error // Custom function for generating HCL syntax for the resource
}

type TerraformResourceAssociation struct {
	*TerraformResourceHints
	datasourceQueryParams map[string]string // Mapping of data source inputs and the source attribute from a parent resource
}

// Wrapper around string value to differentiate strings from interpolations
// Differentiation needed to write oci_resource.resource_name vs "oci_resource.resource_name" for v0.12
type InterpolationString struct {
	value string
}

type GenerateConfigStep struct {
	root                *OCIResource
	resourceGraph       TerraformResourceGraph
	stepName            string
	discoveredResources []*OCIResource
	omittedResources    []*OCIResource
}

type TerraformResourceGraph map[string][]TerraformResourceAssociation

type ProcessOCIResourcesFunc func(*OracleClients, []*OCIResource) ([]*OCIResource, error)

func init() {
	// TODO: The following changes to resource hints are deviations from what can currently be handled by the core resource discovery/generation logic
	// We should strive to eliminate these deviations by either improving the core logic or code generator

	// Custom overrides for generating composite Load Balancer IDs within the resource discovery framework
	exportLoadBalancerBackendHints.processDiscoveredResourcesFn = processLoadBalancerBackends
	exportLoadBalancerBackendSetHints.processDiscoveredResourcesFn = processLoadBalancerBackendSets
	exportLoadBalancerCertificateHints.processDiscoveredResourcesFn = processLoadBalancerCertificates
	exportLoadBalancerHostnameHints.processDiscoveredResourcesFn = processLoadBalancerHostnames
	exportLoadBalancerListenerHints.findResourcesOverrideFn = findLoadBalancerListeners
	exportLoadBalancerPathRouteSetHints.processDiscoveredResourcesFn = processLoadBalancerPathRouteSets
	exportLoadBalancerRuleSetHints.processDiscoveredResourcesFn = processLoadBalancerRuleSets

	exportCoreBootVolumeHints.processDiscoveredResourcesFn = filterSourcedBootVolumes
	exportCoreCrossConnectGroupHints.discoverableLifecycleStates = append(exportCoreCrossConnectGroupHints.discoverableLifecycleStates, string(oci_core.CrossConnectGroupLifecycleStateInactive))
	exportCoreDhcpOptionsHints.processDiscoveredResourcesFn = processDefaultDhcpOptions
	exportCoreImageHints.processDiscoveredResourcesFn = filterCustomImages
	exportCoreInstanceHints.discoverableLifecycleStates = append(exportCoreInstanceHints.discoverableLifecycleStates, string(oci_core.InstanceLifecycleStateStopped))
	exportCoreInstanceHints.processDiscoveredResourcesFn = processInstances
	exportCoreInstanceHints.requireResourceRefresh = true
	exportCoreNetworkSecurityGroupSecurityRuleHints.datasourceClass = "oci_core_network_security_group_security_rules"
	exportCoreNetworkSecurityGroupSecurityRuleHints.datasourceItemsAttr = "security_rules"
	exportCoreNetworkSecurityGroupSecurityRuleHints.processDiscoveredResourcesFn = processNetworkSecurityGroupRules
	exportCoreRouteTableHints.processDiscoveredResourcesFn = processDefaultRouteTables
	exportCoreSecurityListHints.processDiscoveredResourcesFn = processDefaultSecurityLists
	exportCoreVnicAttachmentHints.processDiscoveredResourcesFn = filterSecondaryVnicAttachments
	exportCoreVolumeGroupHints.processDiscoveredResourcesFn = processVolumeGroups

	exportDatabaseAutonomousContainerDatabaseHints.requireResourceRefresh = true
	exportDatabaseAutonomousDatabaseHints.requireResourceRefresh = true
	exportDatabaseAutonomousExadataInfrastructureHints.requireResourceRefresh = true
	exportDatabaseDbSystemHints.requireResourceRefresh = true
	exportDatabaseDbHomeHints.processDiscoveredResourcesFn = filterPrimaryDbHomes
	exportDatabaseDbHomeHints.requireResourceRefresh = true

	exportIdentityAvailabilityDomainHints.resourceAbbreviation = "ad"
	exportIdentityAvailabilityDomainHints.alwaysExportable = true
	exportIdentityAvailabilityDomainHints.processDiscoveredResourcesFn = processAvailabilityDomains
	exportIdentityAvailabilityDomainHints.getHCLStringOverrideFn = getAvailabilityDomainHCLDatasource
	exportIdentityAuthenticationPolicyHints.processDiscoveredResourcesFn = processIdentityAuthenticationPolicies
	exportIdentityTagHints.findResourcesOverrideFn = findIdentityTags
	exportIdentityTagHints.processDiscoveredResourcesFn = processTagDefinitions

	exportObjectStorageNamespaceHints.processDiscoveredResourcesFn = processObjectStorageNamespace
	exportObjectStorageNamespaceHints.getHCLStringOverrideFn = getObjectStorageNamespaceHCLDatasource
	exportObjectStorageNamespaceHints.alwaysExportable = true

	exportObjectStorageBucketHints.getIdFn = getObjectStorageBucketId

	exportContainerengineNodePoolHints.processDiscoveredResourcesFn = processContainerengineNodePool
}

func processContainerengineNodePool(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, nodePool := range resources {
		// subnet_ids and quantity_per_subnet are deprecated and conflict with node_config_details
		if _, exists := nodePool.sourceAttributes["node_config_details"]; exists {
			if _, ok := nodePool.sourceAttributes["subnet_ids"]; ok {
				delete(nodePool.sourceAttributes, "subnet_ids")
			}
			if _, ok := nodePool.sourceAttributes["quantity_per_subnet"]; ok {
				delete(nodePool.sourceAttributes, "quantity_per_subnet")
			}
		}
	}
	return resources, nil
}

// Custom functions to alter behavior of resource discovery and resource HCL representation

func processInstances(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	results := []*OCIResource{}

	for _, instance := range resources {
		// Omit any resources that were launched by an instance pool. Those shouldn't be managed by Terraform as they are created
		// and managed through the instance pool resource instead.
		if freeformTags, exists := instance.sourceAttributes["freeform_tags"]; exists {
			if freeformTagMap, ok := freeformTags.(map[string]interface{}); ok {
				if _, hasInstancePoolTag := freeformTagMap["oci:compute:instancepool"]; hasInstancePoolTag {
					continue
				}
			}
		}

		// Ensure the boot volume created by this instance can be referenced elsewhere by adding it to the reference map
		if bootVolumeId, exists := instance.sourceAttributes["boot_volume_id"]; exists {
			if bootVolumeIdStr, ok := bootVolumeId.(string); ok {
				referenceMap[bootVolumeIdStr] = tfHclVersion.getDoubleExpHclString(instance.getTerraformReference(), "boot_volume_id")
			}
		}

		if rawSourceDetailsList, sourceDetailsExist := instance.sourceAttributes["source_details"]; sourceDetailsExist {
			if sourceDetailList, ok := rawSourceDetailsList.([]interface{}); ok && len(sourceDetailList) > 0 {
				if sourceDetails, ok := sourceDetailList[0].(map[string]interface{}); ok {
					if imageId, ok := instance.sourceAttributes["image"].(string); ok {
						sourceDetails["source_id"] = imageId

						// The image OCID may be different if it's in a different tenancy or region, add a variable for users to specify
						imageVarName := fmt.Sprintf("%s_source_image_id", instance.terraformName)
						vars[imageVarName] = fmt.Sprintf("\"%s\"", imageId)
						referenceMap[imageId] = tfHclVersion.getVarHclString(imageVarName)
					}

					// Workaround for service limitation. Service returns 47GB size for boot volume but LaunchInstance can only
					// accept sizes 50GB and above. If such a situation arises, fall back to service default values for boot volume size.
					if bootVolumeSizeInGbs, exists := sourceDetails["boot_volume_size_in_gbs"]; exists {
						bootVolumeSize, err := strconv.ParseInt(bootVolumeSizeInGbs.(string), 10, 64)
						if err != nil {
							return resources, err
						}

						if bootVolumeSize < 50 {
							delete(sourceDetails, "boot_volume_size_in_gbs")
						}
					}
				}
			}
		}

		results = append(results, instance)
	}

	return results, nil
}

func filterSecondaryVnicAttachments(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	results := []*OCIResource{}

	for _, attachment := range resources {
		// Filter out any primary vnics, as it's not necessary to create separate TF resources for those.
		datasourceSchema := datasourcesMap["oci_core_vnic"]
		if vnicReadFn := datasourceSchema.Read; vnicReadFn != nil {
			d := datasourceSchema.TestResourceData()
			d.Set("vnic_id", attachment.sourceAttributes["vnic_id"].(string))
			if err := vnicReadFn(d, clients); err != nil {
				return results, err
			}

			if isPrimaryVnic, ok := d.GetOkExists("is_primary"); ok && isPrimaryVnic.(bool) {
				continue
			}
		}

		resourceSchema := resourcesMap["oci_core_vnic_attachment"]
		if vnicAttachmentReadFn := resourceSchema.Read; vnicAttachmentReadFn != nil {
			d := resourceSchema.TestResourceData()
			d.SetId(attachment.id)
			if err := vnicAttachmentReadFn(d, clients); err != nil {
				return results, err
			}
			attachment.sourceAttributes = convertResourceDataToMap(resourceSchema.Schema, d)
		}

		results = append(results, attachment)
	}

	return results, nil
}

func filterSourcedBootVolumes(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	results := []*OCIResource{}

	// Filter out boot volumes that don't have source details. We cannot create boot volumes unless they have source details.
	for _, bootVolume := range resources {
		sourceDetails, exists := bootVolume.sourceAttributes["source_details"]
		if !exists {
			continue
		}

		if sourceDetailsList, ok := sourceDetails.([]interface{}); !ok || len(sourceDetailsList) == 0 {
			continue
		}

		results = append(results, bootVolume)
	}

	return results, nil
}

func processAvailabilityDomains(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for idx, ad := range resources {
		ad.sourceAttributes["index"] = idx + 1

		adName, ok := ad.sourceAttributes["name"].(string)
		if !ok || adName == "" {
			return resources, fmt.Errorf("[ERROR] availability domain at index '%v' has no name\n", idx)
		}
		referenceMap[adName] = tfHclVersion.getDataSourceHclString(ad.getTerraformReference(), "name")
	}

	return resources, nil
}

func processObjectStorageNamespace(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, ns := range resources {
		namespaceName, ok := ns.sourceAttributes["namespace"].(string)
		if !ok || namespaceName == "" {
			return resources, fmt.Errorf("[ERROR] object storage namespace data source has no name\n")
		}
		referenceMap[namespaceName] = tfHclVersion.getDataSourceHclString(ns.getTerraformReference(), "namespace")
	}

	return resources, nil
}

func getAvailabilityDomainHCLDatasource(builder *strings.Builder, ociRes *OCIResource, varMap map[string]string) error {
	builder.WriteString(fmt.Sprintf("data %s %s {\n", ociRes.terraformClass, ociRes.terraformName))

	builder.WriteString(fmt.Sprintf("compartment_id = %v\n", varMap[ociRes.compartmentId]))

	adIndex, ok := ociRes.sourceAttributes["index"]
	if !ok {
		return fmt.Errorf("[ERROR] no index found for availability domain '%s'", ociRes.getTerraformReference())
	}
	builder.WriteString(fmt.Sprintf("ad_number = \"%v\"\n", adIndex.(int)))
	builder.WriteString("}\n")

	return nil
}

func getObjectStorageNamespaceHCLDatasource(builder *strings.Builder, ociRes *OCIResource, varMap map[string]string) error {
	builder.WriteString(fmt.Sprintf("data %s %s {\n", ociRes.terraformClass, ociRes.terraformName))
	builder.WriteString(fmt.Sprintf("compartment_id = %v\n", varMap[ociRes.compartmentId]))
	builder.WriteString("}\n")

	return nil
}

func filterCustomImages(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	results := []*OCIResource{}

	// Filter out official images that are predefined by Oracle. We cannot manage such images in Terraform.
	// Official images have a null or empty compartment ID.
	for _, image := range resources {
		compartmentId, exists := image.sourceAttributes["compartment_id"]
		if !exists {
			continue
		}

		if compartmentIdString, ok := compartmentId.(string); !ok || len(compartmentIdString) == 0 {
			continue
		}

		results = append(results, image)
	}

	return results, nil
}

func processVolumeGroups(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	// Replace the volume group's source details volume list with the actual volume list
	// The source details only captures the list of volumes that were known when the group was created.
	// Additional volumes may have been added since and should be part of the source_details that we generate.
	// TODO: This is a shortcoming that should be addressed by the service and/or the Terraform
	for _, group := range resources {
		volumeIdsRaw, exists := group.sourceAttributes["volume_ids"]
		if !exists {
			continue
		}

		if volumeIds, ok := volumeIdsRaw.([]interface{}); ok && len(volumeIds) > 0 {
			sourceDetailsRaw, detailsExist := group.sourceAttributes["source_details"]
			if !detailsExist {
				continue
			}

			sourceDetails := sourceDetailsRaw.([]interface{})[0].(map[string]interface{})
			sourceDetails["volume_ids"] = volumeIds
		}
	}

	return resources, nil
}

func processLoadBalancerBackendSets(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, backendSet := range resources {
		backendSetName := backendSet.sourceAttributes["name"].(string)
		backendSet.id = getBackendSetCompositeId(backendSetName, backendSet.parent.id)
		backendSet.sourceAttributes["load_balancer_id"] = backendSet.parent.id
	}

	return resources, nil
}

func processLoadBalancerBackends(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, backend := range resources {
		backend.id = getBackendCompositeId(backend.sourceAttributes["name"].(string), backend.parent.sourceAttributes["name"].(string), backend.parent.sourceAttributes["load_balancer_id"].(string))
		backend.sourceAttributes["load_balancer_id"] = backend.parent.sourceAttributes["load_balancer_id"].(string)

		// Don't use references to parent resources if they will be omitted from final result
		if !backend.parent.omitFromExport {
			backend.sourceAttributes["backendset_name"] = InterpolationString{tfHclVersion.getDoubleExpHclString(backend.parent.getTerraformReference(), "name")}
		} else {
			backend.sourceAttributes["backendset_name"] = backend.parent.sourceAttributes["name"].(string)
		}
	}

	return resources, nil
}

func processLoadBalancerHostnames(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, hostname := range resources {
		hostname.id = getHostnameCompositeId(hostname.parent.id, hostname.sourceAttributes["name"].(string))
		hostname.sourceAttributes["load_balancer_id"] = hostname.parent.id
	}

	return resources, nil
}

func processLoadBalancerPathRouteSets(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, pathRouteSet := range resources {
		pathRouteSet.id = getPathRouteSetCompositeId(pathRouteSet.parent.id, pathRouteSet.sourceAttributes["name"].(string))
		pathRouteSet.sourceAttributes["load_balancer_id"] = pathRouteSet.parent.id
	}

	return resources, nil
}

func processLoadBalancerRuleSets(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, ruleSet := range resources {
		ruleSet.id = getRuleSetCompositeId(ruleSet.parent.id, ruleSet.sourceAttributes["name"].(string))
		ruleSet.sourceAttributes["load_balancer_id"] = ruleSet.parent.id
	}

	return resources, nil
}

func processLoadBalancerCertificates(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, certificate := range resources {
		certificate.id = getCertificateCompositeId(certificate.sourceAttributes["certificate_name"].(string), certificate.parent.id)
		certificate.sourceAttributes["load_balancer_id"] = certificate.parent.id
	}

	return resources, nil
}

func getObjectStorageBucketId(resource *OCIResource) (string, error) {
	name, ok := resource.sourceAttributes["name"].(string)
	if !ok {
		return "", fmt.Errorf("[ERROR] unable to find name for bucket id")
	}

	namespace, ok := resource.sourceAttributes["namespace"].(string)
	if !ok {
		return "", fmt.Errorf("[ERROR] unable to find namespace for bucket id")
	}

	return getBucketCompositeId(name, namespace), nil
}

func findIdentityTags(clients *OracleClients, tfMeta *TerraformResourceAssociation, parent *OCIResource) ([]*OCIResource, error) {
	// List on Tags does not return validator, and resource Read requires tagNamespaceId
	// which is also not returned in Summary response. Tags also do not have composite id in state.
	// Getting tags using ListTagsRequest and the calling tagResource.Read
	tagNamespaceId := parent.id
	request := oci_identity.ListTagsRequest{}

	request.TagNamespaceId = &tagNamespaceId

	request.RequestMetadata.RetryPolicy = getRetryPolicy(true, "identity")
	results := []*OCIResource{}

	response, err := clients.identityClient().ListTags(context.Background(), request)
	if err != nil {
		return results, err
	}

	request.Page = response.OpcNextPage

	for request.Page != nil {
		listResponse, err := clients.identityClient().ListTags(context.Background(), request)
		if err != nil {
			return results, err
		}

		response.Items = append(response.Items, listResponse.Items...)
		request.Page = listResponse.OpcNextPage
	}

	for _, tag := range response.Items {
		tagResource := resourcesMap[tfMeta.resourceClass]

		d := tagResource.TestResourceData()
		d.Set("tag_namespace_id", parent.id)
		d.Set("name", tag.Name)

		if err := tagResource.Read(d, clients); err != nil {
			return results, err
		}

		resource := &OCIResource{
			compartmentId:    parent.compartmentId,
			sourceAttributes: convertResourceDataToMap(tagResource.Schema, d),
			rawResource:      tag,
			TerraformResource: TerraformResource{
				id:             d.Id(),
				terraformClass: tfMeta.resourceClass,
				terraformName:  fmt.Sprintf("%s_%s", parent.parent.terraformName, *tag.Name),
			},
			getHclStringFn: getHclStringFromGenericMap,
			parent:         parent,
		}

		results = append(results, resource)
	}

	return results, nil

}

func processTagDefinitions(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, resource := range resources {
		resource.sourceAttributes["tag_namespace_id"] = resource.parent.id
		resource.importId = fmt.Sprintf("tagNamespaces/%s/tags/%s", resource.parent.id, resource.sourceAttributes["name"].(string))
	}
	return resources, nil
}

func findLoadBalancerListeners(clients *OracleClients, tfMeta *TerraformResourceAssociation, parent *OCIResource) ([]*OCIResource, error) {
	loadBalancerId := parent.sourceAttributes["load_balancer_id"].(string)
	backendSetName := parent.sourceAttributes["name"].(string)

	request := oci_load_balancer.GetLoadBalancerRequest{}
	request.LoadBalancerId = &loadBalancerId
	request.RequestMetadata.RetryPolicy = getRetryPolicy(true, "load_balancer")

	response, err := clients.loadBalancerClient().GetLoadBalancer(context.Background(), request)
	if err != nil {
		return nil, err
	}

	listenerResource := resourcesMap[tfMeta.resourceClass]

	results := []*OCIResource{}
	for listenerName, listener := range response.LoadBalancer.Listeners {
		if *listener.DefaultBackendSetName != backendSetName {
			continue
		}

		d := listenerResource.TestResourceData()
		d.SetId(getListenerCompositeId(listenerName, loadBalancerId))

		// This calls into the listener resource's Read fn which has the unfortunate implementation of
		// calling GetLoadBalancer and looping through the listeners to find the expected one. So this entire method
		// may require O(n^^2) time. However, the benefits of having Read populate the ResourceData struct is better than duplicating it here.
		if err := listenerResource.Read(d, clients); err != nil {
			return results, err
		}

		resource := &OCIResource{
			compartmentId:    parent.compartmentId,
			sourceAttributes: convertResourceDataToMap(listenerResource.Schema, d),
			rawResource:      listener,
			TerraformResource: TerraformResource{
				id:             d.Id(),
				terraformClass: tfMeta.resourceClass,
				terraformName:  fmt.Sprintf("%s_%s", parent.parent.terraformName, listenerName),
			},
			getHclStringFn: getHclStringFromGenericMap,
			parent:         parent,
		}

		if !parent.omitFromExport {
			resource.sourceAttributes["default_backend_set_name"] = InterpolationString{tfHclVersion.getDoubleExpHclString(parent.getTerraformReference(), "name")}
		} else {
			resource.sourceAttributes["default_backend_set_name"] = parent.sourceAttributes["name"].(string)
		}
		results = append(results, resource)
	}

	return results, nil
}

func processNetworkSecurityGroupRules(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	for _, resource := range resources {
		resource.sourceAttributes["network_security_group_id"] = resource.parent.id
	}
	return resources, nil
}

func filterPrimaryDbHomes(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	results := []*OCIResource{}
	for _, resource := range resources {
		// Only return dbHome resources that don't match the db home ID of the db system resource.
		if dbHomes, ok := resource.parent.sourceAttributes["db_home"].([]interface{}); ok && len(dbHomes) > 0 {
			if primaryDbHome, ok := dbHomes[0].(map[string]interface{}); ok {
				if primaryDbHomeId, ok := primaryDbHome["id"]; ok && primaryDbHomeId.(string) != resource.id {
					results = append(results, resource)
				}
			}
		}
	}
	return results, nil
}

func processIdentityAuthenticationPolicies(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	// Add composite id as the resource's import ID
	for _, resource := range resources {
		resource.importId = getAuthenticationPolicyCompositeId(resource.compartmentId)
		resource.id = resource.importId
	}
	return resources, nil
}

func processDefaultSecurityLists(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	// Default security lists need to be handled as default resources
	for _, resource := range resources {
		if resource.id == resource.parent.sourceAttributes["default_security_list_id"].(string) {
			resource.sourceAttributes["manage_default_resource_id"] = resource.id
			resource.TerraformResource.terraformClass = "oci_core_default_security_list"

			// Don't use references to parent resources if they will be omitted from final result
			if !resource.parent.omitFromExport {
				resource.TerraformResource.terraformReferenceIdString = fmt.Sprintf("%s.%s", resource.parent.getTerraformReference(), "default_security_list_id")
			}
		}
	}
	return resources, nil
}

func processDefaultRouteTables(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	// Default route tables need to be handled as default resources
	for _, resource := range resources {
		if resource.id == resource.parent.sourceAttributes["default_route_table_id"].(string) {
			resource.sourceAttributes["manage_default_resource_id"] = resource.id
			resource.TerraformResource.terraformClass = "oci_core_default_route_table"

			// Don't use references to parent resources if they will be omitted from final result
			if !resource.parent.omitFromExport {
				resource.TerraformResource.terraformReferenceIdString = fmt.Sprintf("%s.%s", resource.parent.getTerraformReference(), "default_route_table_id")
			}
		}
	}
	return resources, nil
}

func processDefaultDhcpOptions(clients *OracleClients, resources []*OCIResource) ([]*OCIResource, error) {
	// Default dhcp options need to be handled as default resources
	for _, resource := range resources {
		if resource.id == resource.parent.sourceAttributes["default_dhcp_options_id"].(string) {
			resource.sourceAttributes["manage_default_resource_id"] = resource.id
			resource.TerraformResource.terraformClass = "oci_core_default_dhcp_options"

			// Don't use references to parent resources if they will be omitted from final result
			if !resource.parent.omitFromExport {
				resource.TerraformResource.terraformReferenceIdString = fmt.Sprintf("%s.%s", resource.parent.getTerraformReference(), "default_dhcp_options_id")
			}
		}
	}
	return resources, nil
}
